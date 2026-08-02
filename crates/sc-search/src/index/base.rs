//! `base.idx` — the immutable block-compressed trigram segment (§4.1).
//!
//! ```text
//! header (128 B)
//! block directory   block_count × { u64 offset, u32 comp_len, u32 raw_len }
//! trigram dictionary  trigram_count × { [u8;3] trigram, u8 flags, u32 off, u32 len }   (sorted)
//! posting lists     delta + varint encoded block ids
//! blocks            zstd frames, `block_size` names each, built in tree order
//! ```
//!
//! Three things make this small, and all three are load-bearing:
//!
//! 1. **Postings point at blocks, not documents.** Grouping 32 names into one
//!    posting element makes every posting list 32× shorter. plocate's
//!    `--block-size` default is exactly 32.
//! 2. **Blocks are compressed together, in tree order.** `IMG_0001.jpg` and
//!    `IMG_0002.jpg` are adjacent, so the shared prefix nearly vanishes. This
//!    is why the builder sorts before chunking — random order costs multiples.
//! 3. **No position information.** Google Code Search made the same call.
//!    Filename matching does not need offsets, and our ranking is name-match
//!    based rather than BM25, so it does not need term frequencies either.
//!
//! The price is **false positives**: a block whose postings intersect may hold
//! no actual match. They are filtered by decompressing the block and linearly
//! scanning its ≤ `block_size` names, which is exactly the trade plocate
//! documents.

use std::collections::BTreeMap;
use std::fs::File;
use std::io::{BufWriter, Write};
use std::path::Path;

use anyhow::{bail, Context, Result};
use memmap2::Mmap;

use crate::codec;
use crate::fold;
use crate::varint;
use crate::vfs::ShareId;

pub const MAGIC: [u8; 4] = *b"SCNB";
pub const VERSION: u32 = 1;
pub const HEADER_LEN: usize = 128;

/// `{ u64 offset, u32 comp_len, u32 raw_len }`
pub const BLOCKDIR_ENTRY: usize = 16;
/// `{ [u8;3] trigram, u8 flags, u32 post_off, u32 post_len }`
pub const DICT_ENTRY: usize = 12;

pub const FLAG_PRUNED: u8 = 1;

/// High-df pruning is meaningless below a handful of blocks: with 3 blocks
/// every trigram present in 2 of them is at 67% df and would be dropped,
/// leaving nothing to intersect. Pruning only starts once "60% of the blocks"
/// is a statement about selectivity rather than an artefact of a tiny corpus.
pub const MIN_BLOCKS_FOR_PRUNE: u32 = 16;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BlockEntry {
    pub share: ShareId,
    pub path: String,
}

/// Encode one block's payload: the names themselves, which is what makes the
/// index self-contained — no `node` rows required (§4.1, "independent of
/// `node`").
pub fn encode_block(entries: &[(ShareId, String)]) -> Vec<u8> {
    let mut out = Vec::with_capacity(entries.iter().map(|(_, p)| p.len() + 4).sum());
    for (share, path) in entries {
        varint::put(&mut out, share.0 as u64);
        varint::put(&mut out, path.len() as u64);
        out.extend_from_slice(path.as_bytes());
    }
    out
}

pub fn decode_block(payload: &[u8]) -> Result<Vec<BlockEntry>> {
    let mut out = Vec::new();
    let mut pos = 0usize;
    while pos < payload.len() {
        let share = varint::get(payload, &mut pos).context("block: truncated share id")?;
        let len = varint::get(payload, &mut pos).context("block: truncated name length")? as usize;
        let end = pos.checked_add(len).context("block: name length overflow")?;
        if end > payload.len() {
            bail!("block: name runs past end of payload");
        }
        let path = String::from_utf8_lossy(&payload[pos..end]).into_owned();
        pos = end;
        out.push(BlockEntry {
            share: ShareId(share as u32),
            path,
        });
    }
    Ok(out)
}

/// Build `base.idx` at `path`. `entries` must already be in tree order —
/// [`crate::index::tree_order`] does that, and it is not optional: block
/// compression is the whole reason the index is small, and it only works when
/// adjacent names share prefixes.
///
/// Returns the file size in bytes.
pub fn write_base(
    path: &Path,
    entries: &[(ShareId, String)],
    block_size: u32,
    prune_df_ratio: f32,
) -> Result<u64> {
    let block_size = block_size.max(1) as usize;
    let block_count = entries.len().div_ceil(block_size);
    if block_count > u32::MAX as usize {
        bail!("too many blocks for a u32 block id");
    }

    // ---- blocks + per-block trigram sets -----------------------------------
    let mut blocks_buf: Vec<u8> = Vec::new();
    let mut blockdir: Vec<u8> = Vec::with_capacity(block_count * BLOCKDIR_ENTRY);
    let mut postings: BTreeMap<[u8; 3], Vec<u32>> = BTreeMap::new();
    let mut scratch: Vec<[u8; 3]> = Vec::new();

    for (bid, chunk) in entries.chunks(block_size).enumerate() {
        let bid = bid as u32;

        // Trigrams of the whole block, deduplicated. Folded, so the index
        // never stores a case/normalisation variant twice.
        scratch.clear();
        for (_, p) in chunk {
            let folded = fold::fold_str(p);
            crate::trigram::push_all(&mut scratch, &folded);
        }
        scratch.sort_unstable();
        scratch.dedup();
        for t in &scratch {
            postings.entry(*t).or_default().push(bid);
        }

        let raw = encode_block(chunk);
        let comp = codec::compress(&raw);
        let off = blocks_buf.len() as u64;
        blocks_buf.extend_from_slice(&comp);
        blockdir.extend_from_slice(&off.to_le_bytes());
        blockdir.extend_from_slice(&(comp.len() as u32).to_le_bytes());
        blockdir.extend_from_slice(&(raw.len() as u32).to_le_bytes());
    }

    // ---- dictionary + posting lists, with high-df pruning ------------------
    let block_count_u32 = block_count as u32;
    let prune_above = prune_df_ratio * block_count as f32;
    let can_prune = block_count_u32 >= MIN_BLOCKS_FOR_PRUNE;

    let mut dict: Vec<u8> = Vec::with_capacity(postings.len() * DICT_ENTRY);
    let mut post_buf: Vec<u8> = Vec::new();
    let mut pruned_count: u32 = 0;

    for (tri, ids) in &postings {
        // A trigram in more than `prune_df_ratio` of the blocks carries almost
        // no selectivity while owning the longest posting list — the worst
        // possible bytes-per-bit-of-information. Drop the list; keep the
        // dictionary entry so a query can tell "pruned" from "absent".
        let pruned = can_prune && ids.len() as f32 > prune_above;
        let (off, len) = if pruned {
            pruned_count += 1;
            (0u32, 0u32)
        } else {
            let off = post_buf.len() as u32;
            varint::encode_ascending(&mut post_buf, ids);
            (off, (post_buf.len() - off as usize) as u32)
        };
        dict.extend_from_slice(tri);
        dict.push(if pruned { FLAG_PRUNED } else { 0 });
        dict.extend_from_slice(&off.to_le_bytes());
        dict.extend_from_slice(&len.to_le_bytes());
    }

    // ---- layout ------------------------------------------------------------
    let blockdir_off = HEADER_LEN as u64;
    let blockdir_len = blockdir.len() as u64;
    let dict_off = blockdir_off + blockdir_len;
    let dict_len = dict.len() as u64;
    let post_off = dict_off + dict_len;
    let post_len = post_buf.len() as u64;
    let blocks_off = post_off + post_len;
    let blocks_len = blocks_buf.len() as u64;

    let mut hdr = [0u8; HEADER_LEN];
    hdr[0..4].copy_from_slice(&MAGIC);
    put_u32(&mut hdr, 4, VERSION);
    put_u32(&mut hdr, 8, block_size as u32);
    put_u32(&mut hdr, 12, block_count_u32);
    put_u64(&mut hdr, 16, entries.len() as u64);
    put_u32(&mut hdr, 24, postings.len() as u32);
    put_u32(&mut hdr, 28, pruned_count);
    hdr[32..36].copy_from_slice(&prune_df_ratio.to_le_bytes());
    put_u64(&mut hdr, 40, blockdir_off);
    put_u64(&mut hdr, 48, blockdir_len);
    put_u64(&mut hdr, 56, dict_off);
    put_u64(&mut hdr, 64, dict_len);
    put_u64(&mut hdr, 72, post_off);
    put_u64(&mut hdr, 80, post_len);
    put_u64(&mut hdr, 88, blocks_off);
    put_u64(&mut hdr, 96, blocks_len);

    let mut w = BufWriter::new(File::create(path).with_context(|| format!("create {path:?}"))?);
    w.write_all(&hdr)?;
    w.write_all(&blockdir)?;
    w.write_all(&dict)?;
    w.write_all(&post_buf)?;
    w.write_all(&blocks_buf)?;
    w.flush()?;
    let f = w.into_inner().map_err(|e| e.into_error())?;
    f.sync_all().ok();

    Ok(blocks_off + blocks_len)
}

fn put_u32(b: &mut [u8], at: usize, v: u32) {
    b[at..at + 4].copy_from_slice(&v.to_le_bytes());
}
fn put_u64(b: &mut [u8], at: usize, v: u64) {
    b[at..at + 8].copy_from_slice(&v.to_le_bytes());
}
fn get_u32(b: &[u8], at: usize) -> u32 {
    u32::from_le_bytes(b[at..at + 4].try_into().unwrap())
}
fn get_u64(b: &[u8], at: usize) -> u64 {
    u64::from_le_bytes(b[at..at + 8].try_into().unwrap())
}

#[derive(Debug, PartialEq, Eq)]
pub enum Lookup<'a> {
    /// No block in this segment contains the trigram → the intersection is
    /// empty and the query has no base hits.
    Missing,
    /// Dropped at build time for having no selectivity. The query must ignore
    /// it; if *every* trigram lands here, the caller falls back to T2.
    Pruned,
    Postings(&'a [u8]),
}

/// mmap'd read side. Cheap to open, no parsing up front.
pub struct BaseSegment {
    map: Mmap,
    pub block_size: u32,
    pub block_count: u32,
    pub entry_count: u64,
    pub trigram_count: u32,
    pub pruned_count: u32,
    pub prune_df_ratio: f32,
    pub file_bytes: u64,
    blockdir_off: usize,
    dict_off: usize,
    post_off: usize,
    post_len: usize,
    blocks_off: usize,
    blocks_len: usize,
}

impl BaseSegment {
    pub fn open(path: &Path) -> Result<Self> {
        let f = File::open(path).with_context(|| format!("open {path:?}"))?;
        let file_bytes = f.metadata()?.len();
        // SAFETY: the base segment is immutable by construction — it is only
        // ever replaced by an atomic rename onto a fresh inode, and the merge
        // path drops every mapping before renaming (required on Windows).
        let map = unsafe { Mmap::map(&f) }.with_context(|| format!("mmap {path:?}"))?;
        if map.len() < HEADER_LEN {
            bail!("{path:?}: shorter than a header");
        }
        if map[0..4] != MAGIC {
            bail!("{path:?}: bad magic");
        }
        let version = get_u32(&map, 4);
        if version != VERSION {
            bail!("{path:?}: unsupported index version {version}");
        }
        let seg = Self {
            block_size: get_u32(&map, 8),
            block_count: get_u32(&map, 12),
            entry_count: get_u64(&map, 16),
            trigram_count: get_u32(&map, 24),
            pruned_count: get_u32(&map, 28),
            prune_df_ratio: f32::from_le_bytes(map[32..36].try_into().unwrap()),
            blockdir_off: get_u64(&map, 40) as usize,
            dict_off: get_u64(&map, 56) as usize,
            post_off: get_u64(&map, 72) as usize,
            post_len: get_u64(&map, 80) as usize,
            blocks_off: get_u64(&map, 88) as usize,
            blocks_len: get_u64(&map, 96) as usize,
            file_bytes,
            map,
        };
        seg.validate(path)?;
        Ok(seg)
    }

    fn validate(&self, path: &Path) -> Result<()> {
        let need = |off: usize, len: usize| off.checked_add(len).is_some_and(|e| e <= self.map.len());
        if !need(
            self.blockdir_off,
            self.block_count as usize * BLOCKDIR_ENTRY,
        ) || !need(self.dict_off, self.trigram_count as usize * DICT_ENTRY)
            || !need(self.post_off, self.post_len)
            || !need(self.blocks_off, self.blocks_len)
        {
            bail!("{path:?}: section offsets run past end of file");
        }
        if self.block_size == 0 {
            bail!("{path:?}: zero block size");
        }
        Ok(())
    }

    fn dict_entry(&self, i: usize) -> (&[u8], u8, u32, u32) {
        let at = self.dict_off + i * DICT_ENTRY;
        let e = &self.map[at..at + DICT_ENTRY];
        (&e[0..3], e[3], get_u32(e, 4), get_u32(e, 8))
    }

    pub fn lookup(&self, tri: [u8; 3]) -> Lookup<'_> {
        let n = self.trigram_count as usize;
        let mut lo = 0usize;
        let mut hi = n;
        while lo < hi {
            let mid = (lo + hi) / 2;
            let (t, flags, off, len) = self.dict_entry(mid);
            match t.cmp(&tri[..]) {
                std::cmp::Ordering::Less => lo = mid + 1,
                std::cmp::Ordering::Greater => hi = mid,
                std::cmp::Ordering::Equal => {
                    if flags & FLAG_PRUNED != 0 {
                        return Lookup::Pruned;
                    }
                    let s = self.post_off + off as usize;
                    let e = s + len as usize;
                    if e > self.map.len() {
                        return Lookup::Missing;
                    }
                    return Lookup::Postings(&self.map[s..e]);
                }
            }
        }
        Lookup::Missing
    }

    pub fn postings(&self, tri: [u8; 3]) -> Option<Vec<u32>> {
        match self.lookup(tri) {
            Lookup::Postings(bytes) => varint::decode_ascending(bytes),
            _ => None,
        }
    }

    /// Decompress one block and parse its names. This is where false positives
    /// from the posting intersection get filtered.
    pub fn block(&self, id: u32) -> Result<Vec<BlockEntry>> {
        if id >= self.block_count {
            bail!("block id {id} out of range ({} blocks)", self.block_count);
        }
        let at = self.blockdir_off + id as usize * BLOCKDIR_ENTRY;
        let e = &self.map[at..at + BLOCKDIR_ENTRY];
        let off = get_u64(e, 0) as usize;
        let comp_len = get_u32(e, 8) as usize;
        let raw_len = get_u32(e, 12) as usize;
        let s = self
            .blocks_off
            .checked_add(off)
            .context("block offset overflow")?;
        let end = s.checked_add(comp_len).context("block length overflow")?;
        if end > self.blocks_off + self.blocks_len {
            bail!("block {id} runs past the block region");
        }
        let raw = codec::decompress_hint(&self.map[s..end], raw_len)?;
        decode_block(&raw)
    }

    /// Every live entry, in block order. Used by the merge path.
    pub fn iter_entries(&self) -> impl Iterator<Item = Result<Vec<BlockEntry>>> + '_ {
        (0..self.block_count).map(move |id| self.block(id))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::tempdir;

    fn e(p: &str) -> (ShareId, String) {
        (ShareId(1), p.to_string())
    }

    #[test]
    fn block_payload_roundtrip() {
        let entries = vec![e("a/b.txt"), (ShareId(7), "여름휴가사진.jpg".to_string())];
        let raw = encode_block(&entries);
        let back = decode_block(&raw).unwrap();
        assert_eq!(back.len(), 2);
        assert_eq!(back[0].path, "a/b.txt");
        assert_eq!(back[1].share, ShareId(7));
        assert_eq!(back[1].path, "여름휴가사진.jpg");
    }

    #[test]
    fn truncated_block_payload_errors() {
        let raw = encode_block(&[e("hello.txt")]);
        assert!(decode_block(&raw[..raw.len() - 3]).is_err());
    }

    #[test]
    fn header_roundtrip_and_lookup() {
        let dir = tempdir().unwrap();
        let path = dir.path().join("base.idx");
        let entries: Vec<_> = (0..100).map(|i| e(&format!("photos/IMG_{i:04}.jpg"))).collect();
        write_base(&path, &entries, 32, 0.60).unwrap();

        let seg = BaseSegment::open(&path).unwrap();
        assert_eq!(seg.block_size, 32);
        assert_eq!(seg.block_count, 4);
        assert_eq!(seg.entry_count, 100);

        // "img" appears in every block, but with only 4 blocks pruning is off.
        let ids = seg.postings(*b"img").unwrap();
        assert_eq!(ids, vec![0, 1, 2, 3]);
        assert_eq!(seg.lookup(*b"zzz"), Lookup::Missing);

        let b0 = seg.block(0).unwrap();
        assert_eq!(b0.len(), 32);
        assert_eq!(b0[0].path, "photos/IMG_0000.jpg");
    }

    #[test]
    fn high_df_trigrams_are_pruned_once_there_are_enough_blocks() {
        let dir = tempdir().unwrap();
        let path = dir.path().join("base.idx");
        // 64 blocks of 32. "com" (from "common/") is in every block; each
        // "uniqNNNN" token is in exactly one.
        let entries: Vec<_> = (0..2048)
            .map(|i| e(&format!("common/uniq{i:06}.dat")))
            .collect();
        write_base(&path, &entries, 32, 0.60).unwrap();
        let seg = BaseSegment::open(&path).unwrap();
        assert_eq!(seg.block_count, 64);
        assert_eq!(seg.lookup(*b"com"), Lookup::Pruned);
        assert!(seg.pruned_count > 0);
        // A selective trigram survives.
        assert!(matches!(seg.lookup(*b"000"), Lookup::Postings(_)));
    }

    #[test]
    fn empty_index_is_valid() {
        let dir = tempdir().unwrap();
        let path = dir.path().join("base.idx");
        write_base(&path, &[], 32, 0.60).unwrap();
        let seg = BaseSegment::open(&path).unwrap();
        assert_eq!(seg.block_count, 0);
        assert_eq!(seg.entry_count, 0);
        assert_eq!(seg.lookup(*b"abc"), Lookup::Missing);
    }

    #[test]
    fn rejects_garbage() {
        let dir = tempdir().unwrap();
        let path = dir.path().join("bad.idx");
        std::fs::write(&path, b"not an index").unwrap();
        assert!(BaseSegment::open(&path).is_err());
    }
}
