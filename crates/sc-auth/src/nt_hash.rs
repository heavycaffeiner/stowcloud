use anyhow::{anyhow, Result};
use chacha20poly1305::aead::{Aead, KeyInit, Payload};
use chacha20poly1305::{XChaCha20Poly1305, XNonce};
use sc_vfs::UserId;
use md4::{Digest, Md4};

/// `MD4(UTF-16LE(password))`, the NTLM "NT hash".
pub(crate) fn nt_hash(pw: &str) -> [u8; 16] {
    let mut bytes = Vec::with_capacity(pw.len() * 2);
    for unit in pw.encode_utf16() {
        bytes.extend_from_slice(&unit.to_le_bytes());
    }
    let digest = Md4::digest(&bytes);
    let mut out = [0u8; 16];
    out.copy_from_slice(&digest);
    out
}

fn nt_aad(user: UserId, key_ver: u32) -> Vec<u8> {
    let mut aad = Vec::with_capacity(6 + 4 + 4);
    aad.extend_from_slice(b"smb_nt");
    aad.extend_from_slice(&user.get().to_le_bytes());
    aad.extend_from_slice(&key_ver.to_le_bytes());
    aad
}

/// Seals a 16-byte NT hash with XChaCha20-Poly1305 under `master_key`,
/// AAD = ("smb_nt", user_id, key_ver). Output = nonce(24) || ciphertext.
pub(crate) fn seal_nt(master_key: &[u8; 32], nt: &[u8; 16], user: UserId, key_ver: u32) -> Result<Vec<u8>> {
    let cipher = XChaCha20Poly1305::new(master_key.into());
    let mut nonce_bytes = [0u8; 24];
    getrandom::getrandom(&mut nonce_bytes).map_err(|e| anyhow!("getrandom failed: {e}"))?;
    let nonce = XNonce::from_slice(&nonce_bytes);
    let aad = nt_aad(user, key_ver);
    let ct = cipher
        .encrypt(nonce, Payload { msg: nt, aad: &aad })
        .map_err(|_| anyhow!("nt hash seal failed"))?;
    let mut out = Vec::with_capacity(24 + ct.len());
    out.extend_from_slice(&nonce_bytes);
    out.extend_from_slice(&ct);
    Ok(out)
}

/// Opens a blob produced by [`seal_nt`].
pub(crate) fn open_nt(master_key: &[u8; 32], blob: &[u8], user: UserId, key_ver: u32) -> Result<[u8; 16]> {
    if blob.len() < 24 {
        return Err(anyhow!("nt hash ciphertext too short"));
    }
    let (nonce_bytes, ct) = blob.split_at(24);
    let cipher = XChaCha20Poly1305::new(master_key.into());
    let nonce = XNonce::from_slice(nonce_bytes);
    let aad = nt_aad(user, key_ver);
    let pt = cipher
        .decrypt(nonce, Payload { msg: ct, aad: &aad })
        .map_err(|_| anyhow!("nt hash open failed"))?;
    if pt.len() != 16 {
        return Err(anyhow!("nt hash plaintext wrong length"));
    }
    let mut out = [0u8; 16];
    out.copy_from_slice(&pt);
    Ok(out)
}

fn totp_aad(user: UserId) -> Vec<u8> {
    let mut aad = Vec::with_capacity(4 + 4);
    aad.extend_from_slice(b"totp");
    aad.extend_from_slice(&user.get().to_le_bytes());
    aad
}

pub(crate) fn seal_totp_secret(master_key: &[u8; 32], secret: &[u8], user: UserId) -> Result<Vec<u8>> {
    let cipher = XChaCha20Poly1305::new(master_key.into());
    let mut nonce_bytes = [0u8; 24];
    getrandom::getrandom(&mut nonce_bytes).map_err(|e| anyhow!("getrandom failed: {e}"))?;
    let nonce = XNonce::from_slice(&nonce_bytes);
    let aad = totp_aad(user);
    let ct = cipher
        .encrypt(nonce, Payload { msg: secret, aad: &aad })
        .map_err(|_| anyhow!("totp secret seal failed"))?;
    let mut out = Vec::with_capacity(24 + ct.len());
    out.extend_from_slice(&nonce_bytes);
    out.extend_from_slice(&ct);
    Ok(out)
}

pub(crate) fn open_totp_secret(master_key: &[u8; 32], blob: &[u8], user: UserId) -> Result<Vec<u8>> {
    if blob.len() < 24 {
        return Err(anyhow!("totp secret ciphertext too short"));
    }
    let (nonce_bytes, ct) = blob.split_at(24);
    let cipher = XChaCha20Poly1305::new(master_key.into());
    let nonce = XNonce::from_slice(nonce_bytes);
    let aad = totp_aad(user);
    cipher
        .decrypt(nonce, Payload { msg: ct, aad: &aad })
        .map_err(|_| anyhow!("totp secret open failed"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn nt_hash_known_vector() {
        // MD4(UTF-16LE("password")) — well-known NTLM test vector.
        let h = nt_hash("password");
        assert_eq!(
            h,
            [
                0x88, 0x46, 0xf7, 0xea, 0xee, 0x8f, 0xb1, 0x17, 0xad, 0x06, 0xbd, 0xd8, 0x30, 0xb7,
                0x58, 0x6c
            ]
        );
    }

    #[test]
    fn seal_open_roundtrip() {
        let key = [7u8; 32];
        let nt = nt_hash("hunter2hunter2");
        let user = UserId::new(42);
        let blob = seal_nt(&key, &nt, user, 1).unwrap();
        let opened = open_nt(&key, &blob, user, 1).unwrap();
        assert_eq!(nt, opened);
        // wrong AAD (different user) must fail
        assert!(open_nt(&key, &blob, UserId::new(43), 1).is_err());
    }
}
