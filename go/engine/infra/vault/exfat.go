package vault

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// ErrCorruptDirectory means a directory entry set's own checksum, or the
// up-case table's, did not match its content. exFAT carries no equivalent
// concept in FAT, which has no per-entry checksum to fail: refusing rather
// than reading past a mismatch is this driver's only sound response,
// since nothing downstream of it can be trusted with a name or a chain
// this driver cannot verify it decoded correctly.
var ErrCorruptDirectory = errors.New("vault: exFAT directory entry checksum mismatch")

// This file, exfatdir.go and exfatname.go hold an exFAT driver over the
// same Device fat.go defines: VeraCrypt's format wizard offers exFAT on
// every platform it targets, and it is the only one of the three formats
// this backend meets that can hold a single file larger than 4 GiB, which
// makes a container this driver could not read the common case for a
// large volume rather than an edge case.
//
// Structure and naming mirror fat.go, fatdir.go and fatname.go throughout:
// one mutex per mounted filesystem, the same read-modify-write shape for a
// partial sector, the same cluster-chain and free-space bookkeeping, the
// same error sentinels where the concept is not FAT-specific. What differs
// is exFAT itself: allocation state lives in a bitmap rather than in the
// FAT's own zero entries, a directory entry is a variable-length set of
// 32-byte records carrying its own checksum, and a stream extension entry
// can mark its content contiguous and unlinked from the FAT entirely.

// Bounds this driver enforces on an exFAT boot sector it did not write
// itself, since a container's decrypted content is untrusted input the
// moment it did not come from this driver's own FormatExFat.
const (
	minExFatBytesPerSectorShift        = 9  // 512 bytes
	maxExFatBytesPerSectorShift        = 12 // 4096 bytes
	maxExFatSectorShiftSum             = 25 // BytesPerSectorShift + SectorsPerClusterShift, the spec's 32 MiB cluster ceiling
	minExFatClusterCount        uint32 = 1
	maxExFatClusterCount        uint32 = 0xFFFFFFF5
	maxUpcaseTableBytes                = 512 << 10
)

// exFatBootRegionSectors is the fixed size, in sectors, of the main boot
// region: the boot sector itself, eight extended boot sectors, the OEM
// parameters sector, a reserved sector and the boot checksum sector. The
// backup boot region immediately follows and mirrors it exactly.
const exFatBootRegionSectors = 12

// exFatChecksummedSectors is how much of the main boot region the boot
// checksum covers: every sector up to, but not including, the checksum
// sector itself.
const exFatChecksummedSectors = exFatBootRegionSectors - 1

// exfatEOC and exfatBad are the reserved FAT entry values this driver
// recognizes on read. Unlike FAT12/16/32, exFAT never reserves a media
// descriptor value across the whole entry range: cluster 0 and 1's fixed
// entries carry it once, in the FAT itself.
const (
	exfatEOC = 0xFFFFFFFF
	exfatBad = 0xFFFFFFF7
)

// Directory entry type bytes. Bit 0x80 marks an entry in use; clearing it
// is how this driver deletes one without disturbing the type bits a
// scanner uses to know how many slots to skip.
const (
	exEntryBitmap    = 0x81
	exEntryUpcase    = 0x82
	exEntryLabel     = 0x83
	exEntryFile      = 0x85
	exEntryStreamExt = 0xC0
	exEntryFileName  = 0xC1
)

// File attribute bits. exFAT keeps the same meanings FAT gives them, in a
// 16-bit field with no VolumeID bit: a volume label is its own entry type
// here, not a file wearing an attribute.
const (
	exAttrReadOnly  = 0x0001
	exAttrHidden    = 0x0002
	exAttrSystem    = 0x0004
	exAttrDirectory = 0x0010
	exAttrArchive   = 0x0020
)

// Stream extension GeneralSecondaryFlags bits.
const (
	exFlagAllocationPossible = 0x01
	exFlagNoFatChain         = 0x02
)

// defaultExFatUpcaseTableB64 is the standard exFAT up-case table, the same
// bytes every mainstream formatter (including the exfatprogs mkfs.exfat
// this package's own tests format real volumes with) writes: a run-length
// compressed mapping from a BMP code point to its uppercase form, used both
// to fold a name for its NameHash field and to compare names
// case-insensitively. Embedding the standard table rather than compressing
// one of this driver's own means a volume this driver formats carries
// exactly the content every other exFAT implementation expects.
const defaultExFatUpcaseTableB64 = "" +
	"AAABAAIAAwAEAAUABgAHAAgACQAKAAsADAANAA4ADwAQABEAEgATABQAFQAWABcAGAAZABoAGwAcAB0AHgAfACAAIQAiACMAJAAl" +
	"ACYAJwAoACkAKgArACwALQAuAC8AMAAxADIAMwA0ADUANgA3ADgAOQA6ADsAPAA9AD4APwBAAEEAQgBDAEQARQBGAEcASABJAEoA" +
	"SwBMAE0ATgBPAFAAUQBSAFMAVABVAFYAVwBYAFkAWgBbAFwAXQBeAF8AYABBAEIAQwBEAEUARgBHAEgASQBKAEsATABNAE4ATwBQ" +
	"AFEAUgBTAFQAVQBWAFcAWABZAFoAewB8AH0AfgB/AIAAgQCCAIMAhACFAIYAhwCIAIkAigCLAIwAjQCOAI8AkACRAJIAkwCUAJUA" +
	"lgCXAJgAmQCaAJsAnACdAJ4AnwCgAKEAogCjAKQApQCmAKcAqACpAKoAqwCsAK0ArgCvALAAsQCyALMAtAC1ALYAtwC4ALkAugC7" +
	"ALwAvQC+AL8AwADBAMIAwwDEAMUAxgDHAMgAyQDKAMsAzADNAM4AzwDQANEA0gDTANQA1QDWANcA2ADZANoA2wDcAN0A3gDfAMAA" +
	"wQDCAMMAxADFAMYAxwDIAMkAygDLAMwAzQDOAM8A0ADRANIA0wDUANUA1gD3ANgA2QDaANsA3ADdAN4AeAEAAQABAgECAQQBBAEG" +
	"AQYBCAEIAQoBCgEMAQwBDgEOARABEAESARIBFAEUARYBFgEYARgBGgEaARwBHAEeAR4BIAEgASIBIgEkASQBJgEmASgBKAEqASoB" +
	"LAEsAS4BLgEwATEBMgEyATQBNAE2ATYBOAE5ATkBOwE7AT0BPQE/AT8BQQFBAUMBQwFFAUUBRwFHAUkBSgFKAUwBTAFOAU4BUAFQ" +
	"AVIBUgFUAVQBVgFWAVgBWAFaAVoBXAFcAV4BXgFgAWABYgFiAWQBZAFmAWYBaAFoAWoBagFsAWwBbgFuAXABcAFyAXIBdAF0AXYB" +
	"dgF4AXkBeQF7AXsBfQF9AX8BQwKBAYIBggGEAYQBhgGHAYcBiQGKAYsBiwGNAY4BjwGQAZEBkQGTAZQB9gGWAZcBmAGYAT0CmwGc" +
	"AZ0BIAKfAaABoAGiAaIBpAGkAaYBpwGnAakBqgGrAawBrAGuAa8BrwGxAbIBswGzAbUBtQG3AbgBuAG6AbsBvAG8Ab4B9wHAAcEB" +
	"wgHDAcQBxQHEAccByAHHAcoBywHKAc0BzQHPAc8B0QHRAdMB0wHVAdUB1wHXAdkB2QHbAdsBjgHeAd4B4AHgAeIB4gHkAeQB5gHm" +
	"AegB6AHqAeoB7AHsAe4B7gHwAfEB8gHxAfQB9AH2AfcB+AH4AfoB+gH8AfwB/gH+AQACAAICAgICBAIEAgYCBgIIAggCCgIKAgwC" +
	"DAIOAg4CEAIQAhICEgIUAhQCFgIWAhgCGAIaAhoCHAIcAh4CHgIgAiECIgIiAiQCJAImAiYCKAIoAioCKgIsAiwCLgIuAjACMAIy" +
	"AjICNAI1AjYCNwI4AjkCZSw7AjsCPQJmLD8CQAJBAkECQwJEAkUCRgJGAkgCSAJKAkoCTAJMAk4CTgJQAlECUgKBAYYBVQKJAYoB" +
	"WAKPAVoCkAFcAl0CXgJfApMBYQJiApQBZAJlAmYCZwKXAZYBagJiLGwCbQJuApwBcAJxAp0BcwJ0Ap8BdgJ3AngCeQJ6AnsCfAJk" +
	"LH4CfwKmAYECggKpAYQChQKGAocCrgFEArEBsgFFAo0CjgKPApACkQK3AZMClAKVApYClwKYApkCmgKbApwCnQKeAp8CoAKhAqIC" +
	"owKkAqUCpgKnAqgCqQKqAqsCrAKtAq4CrwKwArECsgKzArQCtQK2ArcCuAK5AroCuwK8Ar0CvgK/AsACwQLCAsMCxALFAsYCxwLI" +
	"AskCygLLAswCzQLOAs8C0ALRAtIC0wLUAtUC1gLXAtgC2QLaAtsC3ALdAt4C3wLgAuEC4gLjAuQC5QLmAucC6ALpAuoC6wLsAu0C" +
	"7gLvAvAC8QLyAvMC9AL1AvYC9wL4AvkC+gL7AvwC/QL+Av8CAAMBAwIDAwMEAwUDBgMHAwgDCQMKAwsDDAMNAw4DDwMQAxEDEgMT" +
	"AxQDFQMWAxcDGAMZAxoDGwMcAx0DHgMfAyADIQMiAyMDJAMlAyYDJwMoAykDKgMrAywDLQMuAy8DMAMxAzIDMwM0AzUDNgM3AzgD" +
	"OQM6AzsDPAM9Az4DPwNAA0EDQgNDA0QDRQNGA0cDSANJA0oDSwNMA00DTgNPA1ADUQNSA1MDVANVA1YDVwNYA1kDWgNbA1wDXQNe" +
	"A18DYANhA2IDYwNkA2UDZgNnA2gDaQNqA2sDbANtA24DbwNwA3EDcgNzA3QDdQN2A3cDeAN5A3oD/QP+A/8DfgN/A4ADgQOCA4MD" +
	"hAOFA4YDhwOIA4kDigOLA4wDjQOOA48DkAORA5IDkwOUA5UDlgOXA5gDmQOaA5sDnAOdA54DnwOgA6EDogOjA6QDpQOmA6cDqAOp" +
	"A6oDqwOGA4gDiQOKA7ADkQOSA5MDlAOVA5YDlwOYA5kDmgObA5wDnQOeA58DoAOhA6MDowOkA6UDpgOnA6gDqQOqA6sDjAOOA48D" +
	"zwPQA9ED0gPTA9QD1QPWA9cD2APYA9oD2gPcA9wD3gPeA+AD4APiA+ID5APkA+YD5gPoA+gD6gPqA+wD7APuA+4D8APxA/kD8wP0" +
	"A/UD9gP3A/cD+QP6A/oD/AP9A/4D/wMABAEEAgQDBAQEBQQGBAcECAQJBAoECwQMBA0EDgQPBBAEEQQSBBMEFAQVBBYEFwQYBBkE" +
	"GgQbBBwEHQQeBB8EIAQhBCIEIwQkBCUEJgQnBCgEKQQqBCsELAQtBC4ELwQQBBEEEgQTBBQEFQQWBBcEGAQZBBoEGwQcBB0EHgQf" +
	"BCAEIQQiBCMEJAQlBCYEJwQoBCkEKgQrBCwELQQuBC8EAAQBBAIEAwQEBAUEBgQHBAgECQQKBAsEDAQNBA4EDwRgBGAEYgRiBGQE" +
	"ZARmBGYEaARoBGoEagRsBGwEbgRuBHAEcARyBHIEdAR0BHYEdgR4BHgEegR6BHwEfAR+BH4EgASABIIEgwSEBIUEhgSHBIgEiQSK" +
	"BIoEjASMBI4EjgSQBJAEkgSSBJQElASWBJYEmASYBJoEmgScBJwEngSeBKAEoASiBKIEpASkBKYEpgSoBKgEqgSqBKwErASuBK4E" +
	"sASwBLIEsgS0BLQEtgS2BLgEuAS6BLoEvAS8BL4EvgTABMEEwQTDBMMExQTFBMcExwTJBMkEywTLBM0EzQTABNAE0ATSBNIE1ATU" +
	"BNYE1gTYBNgE2gTaBNwE3ATeBN4E4ATgBOIE4gTkBOQE5gTmBOgE6ATqBOoE7ATsBO4E7gTwBPAE8gTyBPQE9AT2BPYE+AT4BPoE" +
	"+gT8BPwE/gT+BAAFAAUCBQIFBAUEBQYFBgUIBQgFCgUKBQwFDAUOBQ4FEAUQBRIFEgUUBRUFFgUXBRgFGQUaBRsFHAUdBR4FHwUg" +
	"BSEFIgUjBSQFJQUmBScFKAUpBSoFKwUsBS0FLgUvBTAFMQUyBTMFNAU1BTYFNwU4BTkFOgU7BTwFPQU+BT8FQAVBBUIFQwVEBUUF" +
	"RgVHBUgFSQVKBUsFTAVNBU4FTwVQBVEFUgVTBVQFVQVWBVcFWAVZBVoFWwVcBV0FXgVfBWAFMQUyBTMFNAU1BTYFNwU4BTkFOgU7" +
	"BTwFPQU+BT8FQAVBBUIFQwVEBUUFRgVHBUgFSQVKBUsFTAVNBU4FTwVQBVEFUgVTBVQFVQVWBf//9hdjLH4dfx2AHYEdgh2DHYQd" +
	"hR2GHYcdiB2JHYodix2MHY0djh2PHZAdkR2SHZMdlB2VHZYdlx2YHZkdmh2bHZwdnR2eHZ8doB2hHaIdox2kHaUdph2nHagdqR2q" +
	"HasdrB2tHa4drx2wHbEdsh2zHbQdtR22HbcduB25Hbodux28Hb0dvh2/HcAdwR3CHcMdxB3FHcYdxx3IHckdyh3LHcwdzR3OHc8d" +
	"0B3RHdId0x3UHdUd1h3XHdgd2R3aHdsd3B3dHd4d3x3gHeEd4h3jHeQd5R3mHecd6B3pHeod6x3sHe0d7h3vHfAd8R3yHfMd9B31" +
	"HfYd9x34Hfkd+h37Hfwd/R3+Hf8dAB4AHgIeAh4EHgQeBh4GHggeCB4KHgoeDB4MHg4eDh4QHhAeEh4SHhQeFB4WHhYeGB4YHhoe" +
	"Gh4cHhweHh4eHiAeIB4iHiIeJB4kHiYeJh4oHigeKh4qHiweLB4uHi4eMB4wHjIeMh40HjQeNh42HjgeOB46HjoePB48Hj4ePh5A" +
	"HkAeQh5CHkQeRB5GHkYeSB5IHkoeSh5MHkweTh5OHlAeUB5SHlIeVB5UHlYeVh5YHlgeWh5aHlweXB5eHl4eYB5gHmIeYh5kHmQe" +
	"Zh5mHmgeaB5qHmoebB5sHm4ebh5wHnAech5yHnQedB52HnYeeB54Hnoeeh58Hnwefh5+HoAegB6CHoIehB6EHoYehh6IHogeih6K" +
	"HowejB6OHo4ekB6QHpIekh6UHpQelh6XHpgemR6aHpsenB6dHp4enx6gHqAeoh6iHqQepB6mHqYeqB6oHqoeqh6sHqwerh6uHrAe" +
	"sB6yHrIetB60HrYeth64Hrgeuh66HrwevB6+Hr4ewB7AHsIewh7EHsQexh7GHsgeyB7KHsoezB7MHs4ezh7QHtAe0h7SHtQe1B7W" +
	"HtYe2B7YHtoe2h7cHtwe3h7eHuAe4B7iHuIe5B7kHuYe5h7oHuge6h7qHuwe7B7uHu4e8B7wHvIe8h70HvQe9h72Hvge+B76Hvse" +
	"/B79Hv4e/x4IHwkfCh8LHwwfDR8OHw8fCB8JHwofCx8MHw0fDh8PHxgfGR8aHxsfHB8dHxYfFx8YHxkfGh8bHxwfHR8eHx8fKB8p" +
	"HyofKx8sHy0fLh8vHygfKR8qHysfLB8tHy4fLx84HzkfOh87HzwfPR8+Hz8fOB85HzofOx88Hz0fPh8/H0gfSR9KH0sfTB9NH0Yf" +
	"Rx9IH0kfSh9LH0wfTR9OH08fUB9ZH1IfWx9UH10fVh9fH1gfWR9aH1sfXB9dH14fXx9oH2kfah9rH2wfbR9uH28faB9pH2ofax9s" +
	"H20fbh9vH7ofux/IH8kfyh/LH9of2x/4H/kf6h/rH/of+x9+H38fiB+JH4ofix+MH40fjh+PH4gfiR+KH4sfjB+NH44fjx+YH5kf" +
	"mh+bH5wfnR+eH58fmB+ZH5ofmx+cH50fnh+fH6gfqR+qH6sfrB+tH64frx+oH6kfqh+rH6wfrR+uH68fuB+5H7IfvB+0H7Ufth+3" +
	"H7gfuR+6H7sfvB+9H74fvx/AH8Efwh/DH8QfxR/GH8cfyB/JH8ofyx/DH80fzh/PH9gf2R/SH9Mf1B/VH9Yf1x/YH9kf2h/bH9wf" +
	"3R/eH98f6B/pH+If4x/kH+wf5h/nH+gf6R/qH+sf7B/tH+4f7x/wH/Ef8h/zH/Qf9R/2H/cf+B/5H/of+x/zH/0f/h//HwAgASAC" +
	"IAMgBCAFIAYgByAIIAkgCiALIAwgDSAOIA8gECARIBIgEyAUIBUgFiAXIBggGSAaIBsgHCAdIB4gHyAgICEgIiAjICQgJSAmICcg" +
	"KCApICogKyAsIC0gLiAvIDAgMSAyIDMgNCA1IDYgNyA4IDkgOiA7IDwgPSA+ID8gQCBBIEIgQyBEIEUgRiBHIEggSSBKIEsgTCBN" +
	"IE4gTyBQIFEgUiBTIFQgVSBWIFcgWCBZIFogWyBcIF0gXiBfIGAgYSBiIGMgZCBlIGYgZyBoIGkgaiBrIGwgbSBuIG8gcCBxIHIg" +
	"cyB0IHUgdiB3IHggeSB6IHsgfCB9IH4gfyCAIIEggiCDIIQghSCGIIcgiCCJIIogiyCMII0gjiCPIJAgkSCSIJMglCCVIJYglyCY" +
	"IJkgmiCbIJwgnSCeIJ8goCChIKIgoyCkIKUgpiCnIKggqSCqIKsgrCCtIK4gryCwILEgsiCzILQgtSC2ILcguCC5ILoguyC8IL0g" +
	"viC/IMAgwSDCIMMgxCDFIMYgxyDIIMkgyiDLIMwgzSDOIM8g0CDRINIg0yDUINUg1iDXINgg2SDaINsg3CDdIN4g3yDgIOEg4iDj" +
	"IOQg5SDmIOcg6CDpIOog6yDsIO0g7iDvIPAg8SDyIPMg9CD1IPYg9yD4IPkg+iD7IPwg/SD+IP8gACEBIQIhAyEEIQUhBiEHIQgh" +
	"CSEKIQshDCENIQ4hDyEQIREhEiETIRQhFSEWIRchGCEZIRohGyEcIR0hHiEfISAhISEiISMhJCElISYhJyEoISkhKiErISwhLSEu" +
	"IS8hMCExITIhMyE0ITUhNiE3ITghOSE6ITshPCE9IT4hPyFAIUEhQiFDIUQhRSFGIUchSCFJIUohSyFMIU0hMiFPIVAhUSFSIVMh" +
	"VCFVIVYhVyFYIVkhWiFbIVwhXSFeIV8hYCFhIWIhYyFkIWUhZiFnIWghaSFqIWshbCFtIW4hbyFgIWEhYiFjIWQhZSFmIWchaCFp" +
	"IWohayFsIW0hbiFvIYAhgSGCIYMhgyH//0sDtiS3JLgkuSS6JLskvCS9JL4kvyTAJMEkwiTDJMQkxSTGJMckyCTJJMokyyTMJM0k" +
	"ziTPJP//RgcALAEsAiwDLAQsBSwGLAcsCCwJLAosCywMLA0sDiwPLBAsESwSLBMsFCwVLBYsFywYLBksGiwbLBwsHSweLB8sICwh" +
	"LCIsIywkLCUsJiwnLCgsKSwqLCssLCwtLC4sXyxgLGAsYixjLGQsZSxmLGcsZyxpLGksayxrLG0sbixvLHAscSxyLHMsdCx1LHUs" +
	"dyx4LHkseix7LHwsfSx+LH8sgCyALIIsgiyELIQshiyGLIgsiCyKLIosjCyMLI4sjiyQLJAskiySLJQslCyWLJYsmCyYLJosmiyc" +
	"LJwsniyeLKAsoCyiLKIspCykLKYspiyoLKgsqiyqLKwsrCyuLK4ssCywLLIssiy0LLQstiy2LLgsuCy6LLosvCy8LL4svizALMAs" +
	"wizCLMQsxCzGLMYsyCzILMosyizMLMwszizOLNAs0CzSLNIs1CzULNYs1izYLNgs2izaLNws3CzeLN4s4CzgLOIs4izkLOUs5izn" +
	"LOgs6SzqLOss7CztLO4s7yzwLPEs8izzLPQs9Sz2LPcs+Cz5LPos+yz8LP0s/iz/LKAQoRCiEKMQpBClEKYQpxCoEKkQqhCrEKwQ" +
	"rRCuEK8QsBCxELIQsxC0ELUQthC3ELgQuRC6ELsQvBC9EL4QvxDAEMEQwhDDEMQQxRD//xvSIf8i/yP/JP8l/yb/J/8o/yn/Kv8r" +
	"/yz/Lf8u/y//MP8x/zL/M/80/zX/Nv83/zj/Of86/1v/XP9d/17/X/9g/2H/Yv9j/2T/Zf9m/2f/aP9p/2r/a/9s/23/bv9v/3D/" +
	"cf9y/3P/dP91/3b/d/94/3n/ev97/3z/ff9+/3//gP+B/4L/g/+E/4X/hv+H/4j/if+K/4v/jP+N/47/j/+Q/5H/kv+T/5T/lf+W" +
	"/5f/mP+Z/5r/m/+c/53/nv+f/6D/of+i/6P/pP+l/6b/p/+o/6n/qv+r/6z/rf+u/6//sP+x/7L/s/+0/7X/tv+3/7j/uf+6/7v/" +
	"vP+9/77/v//A/8H/wv/D/8T/xf/G/8f/yP/J/8r/y//M/83/zv/P/9D/0f/S/9P/1P/V/9b/1//Y/9n/2v/b/9z/3f/e/9//4P/h" +
	"/+L/4//k/+X/5v/n/+j/6f/q/+v/7P/t/+7/7//w//H/8v/z//T/9f/2//f/+P/5//r/+//8//3//v///w=="

// defaultExFatUpcaseChecksum is the checksum encodeUpcaseTable computes for
// the exact bytes above, verified once in TestDefaultUpcaseTableChecksum:
// it has to match a real formatter's own, or a foreign implementation
// reading a volume this driver formatted would refuse the table.
const defaultExFatUpcaseChecksum = 0xe619d30d

// exfatBounds bounds a raw allocation this driver did not compute itself
// (a cluster count, a byte length derived from one) before it is used to
// size a slice or seek an offset, the same discipline mustNarrow enforces
// for a width conversion.
func exfatBounds(cond bool, format string, args ...any) error {
	if cond {
		return nil
	}
	return fmt.Errorf("%w: "+format, append([]any{ErrUnsupportedFilesystem}, args...)...)
}

// exfatBPB is this driver's parsed view of an exFAT boot sector: the
// fields every other structure in this file addresses the volume through.
type exfatBPB struct {
	volumeLength           uint64 // sectors
	fatOffset              uint32 // sectors from volume start
	fatLength              uint32 // sectors
	clusterHeapOffset      uint32 // sectors from volume start
	clusterCount           uint32
	rootCluster            uint32
	volumeSerial           uint32
	bytesPerSectorShift    uint32
	sectorsPerClusterShift uint32
}

func (b exfatBPB) bytesPerSector() uint32 { return 1 << b.bytesPerSectorShift }
func (b exfatBPB) bytesPerCluster() uint32 {
	return 1 << (b.bytesPerSectorShift + b.sectorsPerClusterShift)
}

// ExFatFS is one mounted exFAT filesystem over a Device.
//
// Every exported method takes mu for its entire body, for the same reason
// fat.go's FS does: two operations racing over one cluster bitmap and FAT
// would corrupt both.
type ExFatFS struct {
	mu sync.Mutex

	dev Device
	bpb exfatBPB
	clk clock.Clock

	bitmapCluster uint32
	bitmapBytes   uint32

	upcaseCluster uint32
	upcase        []uint16 // 65536 entries: upcase[c] is c's uppercase code point

	freeCount uint32
}

func (fs *ExFatFS) sectorOffset(sector uint32) int64 {
	return int64(sector) * int64(fs.bpb.bytesPerSector())
}

// clusterOffset is the byte offset of cluster c's first sector. Cluster
// numbering starts at 2, the same convention FAT uses.
func (fs *ExFatFS) clusterOffset(c uint32) int64 {
	return fs.sectorOffset(fs.bpb.clusterHeapOffset) + int64(c-2)*int64(fs.bpb.bytesPerCluster())
}

func (fs *ExFatFS) readAt(p []byte, off int64) error {
	n, err := fs.dev.ReadAt(p, off)
	if n == len(p) {
		return nil
	}
	if err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

func (fs *ExFatFS) writeAt(p []byte, off int64) error {
	n, err := fs.dev.WriteAt(p, off)
	if n == len(p) {
		return nil
	}
	if err != nil {
		return err
	}
	return io.ErrShortWrite
}

func (fs *ExFatFS) writeCluster(c uint32, buf []byte) error {
	return fs.writeAt(buf, fs.clusterOffset(c))
}

// bootChecksum computes the exFAT boot checksum over data, which must be
// exactly exFatChecksummedSectors sectors of bytesPerSector each: a
// rolling 32-bit rotate-and-add over every byte except the three this
// format allows to change after the fact (VolumeFlags and PercentInUse,
// both in the first sector) without invalidating the checksum.
func bootChecksum(data []byte, bytesPerSector uint32) uint32 {
	var sum uint32
	for i, b := range data {
		if i == 106 || i == 107 || i == 112 {
			continue
		}
		sum = (sum<<31 | sum>>1) + uint32(b)
	}
	return sum
}

// tableChecksum is the same rolling 32-bit rotate-and-add bootChecksum
// uses, but over every byte with no exclusions: the algorithm the exFAT
// specification also uses for the up-case table's own TableChecksum
// field, which has no equivalent of a boot sector's mutable bytes to skip.
func tableChecksum(data []byte) uint32 {
	var sum uint32
	for _, b := range data {
		sum = (sum<<31 | sum>>1) + uint32(b)
	}
	return sum
}

// parseExFatBoot validates and decodes the first sector of a candidate
// exFAT volume. sizeBytes is the actual size of the device backing it, so
// every count the header claims is checked against ground truth rather
// than trusted outright: this content arrived either from a container file
// this process did not write, or from a header field an attacker fully
// controls.
func parseExFatBoot(sector []byte, sizeBytes uint64) (exfatBPB, error) {
	if len(sector) < 512 {
		return exfatBPB{}, fmt.Errorf("%w: boot sector too short", ErrUnsupportedFilesystem)
	}
	if string(sector[3:11]) != "EXFAT   " {
		return exfatBPB{}, fmt.Errorf("%w: not an exFAT boot sector", ErrUnsupportedFilesystem)
	}
	for _, b := range sector[11:64] {
		if b != 0 {
			return exfatBPB{}, fmt.Errorf("%w: MustBeZero region is not zero", ErrUnsupportedFilesystem)
		}
	}
	if sector[510] != 0x55 || sector[511] != 0xAA {
		return exfatBPB{}, fmt.Errorf("%w: missing boot sector signature", ErrUnsupportedFilesystem)
	}

	volumeLength := binary.LittleEndian.Uint64(sector[72:80])
	fatOffset := binary.LittleEndian.Uint32(sector[80:84])
	fatLength := binary.LittleEndian.Uint32(sector[84:88])
	clusterHeapOffset := binary.LittleEndian.Uint32(sector[88:92])
	clusterCount := binary.LittleEndian.Uint32(sector[92:96])
	rootCluster := binary.LittleEndian.Uint32(sector[96:100])
	volumeSerial := binary.LittleEndian.Uint32(sector[100:104])
	fsRevision := sector[105]
	numberOfFats := sector[110]
	bpsShift := uint32(sector[108])
	spcShift := uint32(sector[109])

	if err := exfatBounds(fsRevision == 1, "exFAT major revision %d, want 1", fsRevision); err != nil {
		return exfatBPB{}, err
	}
	if err := exfatBounds(numberOfFats == 1, "%d FATs: TexFAT is not supported", numberOfFats); err != nil {
		return exfatBPB{}, err
	}
	if err := exfatBounds(bpsShift >= minExFatBytesPerSectorShift && bpsShift <= maxExFatBytesPerSectorShift,
		"bytes-per-sector shift %d out of range", bpsShift); err != nil {
		return exfatBPB{}, err
	}
	if err := exfatBounds(bpsShift+spcShift <= maxExFatSectorShiftSum,
		"sectors-per-cluster shift %d too large for a %d-byte sector", spcShift, uint32(1)<<bpsShift); err != nil {
		return exfatBPB{}, err
	}

	bpb := exfatBPB{
		volumeLength:           volumeLength,
		fatOffset:              fatOffset,
		fatLength:              fatLength,
		clusterHeapOffset:      clusterHeapOffset,
		clusterCount:           clusterCount,
		rootCluster:            rootCluster,
		volumeSerial:           volumeSerial,
		bytesPerSectorShift:    bpsShift,
		sectorsPerClusterShift: spcShift,
	}
	bps := uint64(bpb.bytesPerSector())
	bpc := uint64(bpb.bytesPerCluster())

	if err := exfatBounds(volumeLength*bps <= sizeBytes,
		"boot sector claims %d sectors, device holds %d bytes", volumeLength, sizeBytes); err != nil {
		return exfatBPB{}, err
	}
	if err := exfatBounds(clusterCount >= minExFatClusterCount && clusterCount <= maxExFatClusterCount,
		"%d clusters out of range", clusterCount); err != nil {
		return exfatBPB{}, err
	}
	if err := exfatBounds(uint64(fatOffset)+uint64(fatLength) <= uint64(clusterHeapOffset),
		"FAT region overlaps the cluster heap"); err != nil {
		return exfatBPB{}, err
	}
	if err := exfatBounds(uint64(clusterHeapOffset)+uint64(clusterCount)*bpc/bps <= volumeLength,
		"cluster heap larger than the volume"); err != nil {
		return exfatBPB{}, err
	}
	if err := exfatBounds(rootCluster >= 2 && uint64(rootCluster) < uint64(clusterCount)+2,
		"root cluster %d beyond the volume", rootCluster); err != nil {
		return exfatBPB{}, err
	}
	return bpb, nil
}

// verifyBootChecksum re-reads the whole main boot region and confirms its
// checksum sector agrees with what the region itself hashes to: the boot
// sector this driver already parsed could still be a forgery that leaves
// every field individually plausible.
func verifyBootChecksum(dev Device, bps uint32) error {
	region := make([]byte, int64(bps)*exFatChecksummedSectors)
	if n, err := dev.ReadAt(region, 0); err != nil || n != len(region) {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return fmt.Errorf("vault: read exFAT boot region: %w", err)
	}
	want := bootChecksum(region, bps)
	stored := make([]byte, 4)
	if n, err := dev.ReadAt(stored, int64(bps)*exFatChecksummedSectors); err != nil || n != len(stored) {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return fmt.Errorf("vault: read exFAT boot checksum: %w", err)
	}
	if binary.LittleEndian.Uint32(stored) != want {
		return fmt.Errorf("%w: boot checksum mismatch", ErrUnsupportedFilesystem)
	}
	return nil
}

// MountExFat reads and validates the boot sector of dev, sized sizeBytes,
// and brings up the allocation bitmap and up-case table every other
// operation depends on.
func MountExFat(dev Device, sizeBytes uint64, clk clock.Clock) (*ExFatFS, error) {
	sector := make([]byte, 512)
	if n, err := dev.ReadAt(sector, 0); err != nil || n != len(sector) {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("vault: read exFAT boot sector: %w", err)
	}
	bpb, err := parseExFatBoot(sector, sizeBytes)
	if err != nil {
		return nil, err
	}
	if err := verifyBootChecksum(dev, bpb.bytesPerSector()); err != nil {
		return nil, err
	}
	if clk == nil {
		clk = clock.System()
	}
	fs := &ExFatFS{dev: dev, bpb: bpb, clk: clk}
	if err := fs.loadSystemEntries(); err != nil {
		return nil, err
	}
	if err := fs.rescanFreeCount(); err != nil {
		return nil, err
	}
	return fs, nil
}

// Alive re-reads the boot sector and confirms it still parses, without
// touching any content: the same health-probe contract fat.go's Alive
// documents.
func (fs *ExFatFS) Alive() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	sector := make([]byte, 512)
	if err := fs.readAt(sector, 0); err != nil {
		return fmt.Errorf("vault: read exFAT boot sector: %w", err)
	}
	size := fs.bpb.volumeLength * uint64(fs.bpb.bytesPerSector())
	if _, err := parseExFatBoot(sector, size); err != nil {
		return err
	}
	return nil
}

// loadSystemEntries walks the root directory once for the two entries
// every exFAT volume must carry: the allocation bitmap and the up-case
// table. Neither lives at a fixed offset; both are found by their entry
// type, the same way a reader has to find them on a real volume.
func (fs *ExFatFS) loadSystemEntries() error {
	var bitmapFound, upcaseFound bool
	var upcaseFirst uint32
	var upcaseLen uint64
	var wantChecksum uint32
	err := fs.scanRawEntries(fs.bpb.rootCluster, false, 0, func(e exRawEntry, off int64) bool {
		switch e[0] {
		case exEntryBitmap:
			fs.bitmapCluster = binary.LittleEndian.Uint32(e[20:24])
			dataLen := binary.LittleEndian.Uint64(e[24:32])
			fs.bitmapBytes = mustNarrow[uint32](dataLen, "allocation bitmap size")
			bitmapFound = true
		case exEntryUpcase:
			wantChecksum = binary.LittleEndian.Uint32(e[4:8])
			upcaseFirst = binary.LittleEndian.Uint32(e[20:24])
			upcaseLen = binary.LittleEndian.Uint64(e[24:32])
			upcaseFound = true
		}
		return false
	})
	if err != nil {
		return fmt.Errorf("vault: scan root directory for system entries: %w", err)
	}
	if !bitmapFound {
		return fmt.Errorf("%w: no allocation bitmap entry in the root directory", ErrUnsupportedFilesystem)
	}
	if !upcaseFound {
		return fmt.Errorf("%w: no up-case table entry in the root directory", ErrUnsupportedFilesystem)
	}
	fs.upcaseCluster = upcaseFirst
	if berr := exfatBounds(upcaseLen > 0 && upcaseLen <= maxUpcaseTableBytes,
		"up-case table size %d out of range", upcaseLen); berr != nil {
		return berr
	}
	raw := make([]byte, upcaseLen)
	if rerr := fs.readExtent(upcaseFirst, false, upcaseLen, 0, raw); rerr != nil {
		return fmt.Errorf("vault: read up-case table: %w", rerr)
	}
	if tableChecksum(raw) != wantChecksum {
		return fmt.Errorf("%w: up-case table checksum mismatch", ErrCorruptDirectory)
	}
	table, err := decodeUpcaseTable(raw)
	if err != nil {
		return err
	}
	fs.upcase = table
	return nil
}

// decodeUpcaseTable expands a compressed up-case table: a sequence of
// uint16 values, where 0xFFFF introduces a run of the following uint16
// count of code points that map to themselves. 0xFFFF is otherwise never a
// legitimate mapped value, since it is not a Unicode character, which is
// exactly what makes it safe to use as the run marker.
func decodeUpcaseTable(raw []byte) ([]uint16, error) {
	if len(raw)%2 != 0 {
		return nil, fmt.Errorf("%w: up-case table has an odd length", ErrCorruptDirectory)
	}
	table := make([]uint16, 1<<16)
	for i := range table {
		table[i] = uint16(i)
	}
	units := getUTF16Field(raw)
	cur := 0
	for i := 0; i < len(units); i++ {
		v := units[i]
		if v == 0xFFFF {
			// A run marker as the very last unit, with no count after it,
			// is what exfatprogs' own mkfs.exfat writes: harmless, since
			// every code point from here to U+FFFF is already identity by
			// the pre-fill above, so nothing is lost by stopping here.
			if i+1 >= len(units) {
				return table, nil
			}
			i++
			cur += int(units[i])
			if cur > 1<<16 {
				return nil, fmt.Errorf("%w: up-case table run past U+FFFF", ErrCorruptDirectory)
			}
			continue
		}
		if cur >= 1<<16 {
			return nil, fmt.Errorf("%w: up-case table longer than U+FFFF entries", ErrCorruptDirectory)
		}
		table[cur] = v
		cur++
	}
	return table, nil
}

// upcaseRune upcases one BMP code point via the loaded table. A code point
// above U+FFFF, which checkExFatName already refuses in any name this
// driver creates but which a foreign name could still carry, is returned
// unchanged: this driver's table has nothing to say about it.
func (fs *ExFatFS) upcaseRune(c uint16) uint16 { return fs.upcase[c] }

// rescanFreeCount counts clear bits in the allocation bitmap directly, in
// bounded chunks so an oversized bitmap costs disk time, never unbounded
// memory. exFAT keeps no FSInfo-style hint sector, so this is the only way
// to know free space; it runs once, at mount.
func (fs *ExFatFS) rescanFreeCount() error {
	const chunkBytes = 1 << 16
	buf := make([]byte, chunkBytes)
	free := uint32(0)
	remaining := uint64(fs.bitmapBytes)
	for off := uint64(0); off < remaining; off += chunkBytes {
		n := remaining - off
		if n > chunkBytes {
			n = chunkBytes
		}
		chunk := buf[:n]
		if err := fs.readExtent(fs.bitmapCluster, false, remaining, mustNarrow[int64](off, "allocation bitmap scan offset"), chunk); err != nil {
			return fmt.Errorf("vault: scan allocation bitmap: %w", err)
		}
		for _, b := range chunk {
			free += 8 - popcount8(b)
		}
	}
	// The bitmap is byte-granular but the cluster count rarely is; the
	// pad bits past clusterCount are conventionally zero (free) and were
	// just counted as such, so they are subtracted back out here.
	padBits := fs.bitmapBytes*8 - fs.bpb.clusterCount
	fs.freeCount = free - padBits
	return nil
}

func popcount8(b byte) uint32 {
	var n uint32
	for b != 0 {
		n += uint32(b & 1)
		b >>= 1
	}
	return n
}

// bitmapBit reads or sets cluster c's allocation bit. Bit 1 means
// allocated: the opposite convention from FAT12/16/32, where a zero FAT
// entry means free. The FAT itself only ever records chain linkage here.
func (fs *ExFatFS) bitmapBit(c uint32) (bool, error) {
	idx := c - 2
	var b [1]byte
	if err := fs.readExtent(fs.bitmapCluster, false, uint64(fs.bitmapBytes), int64(idx/8), b[:]); err != nil {
		return false, err
	}
	return b[0]&(1<<(idx%8)) != 0, nil
}

func (fs *ExFatFS) setBitmapBit(c uint32, allocated bool) error {
	idx := c - 2
	var b [1]byte
	if err := fs.readExtent(fs.bitmapCluster, false, uint64(fs.bitmapBytes), int64(idx/8), b[:]); err != nil {
		return err
	}
	if allocated {
		b[0] |= 1 << (idx % 8)
	} else {
		b[0] &^= 1 << (idx % 8)
	}
	return fs.writeExtent(&fs.bitmapCluster, int64(idx/8), b[:])
}

// fatEntryOffset is the byte offset of cluster c's 32-bit entry in the FAT.
func (fs *ExFatFS) fatEntryOffset(c uint32) int64 {
	return fs.sectorOffset(fs.bpb.fatOffset) + int64(c)*4
}

func (fs *ExFatFS) getFATEntry(c uint32) (uint32, error) {
	var buf [4]byte
	if err := fs.readAt(buf[:], fs.fatEntryOffset(c)); err != nil {
		return 0, fmt.Errorf("vault: read exFAT FAT entry %d: %w", c, err)
	}
	return binary.LittleEndian.Uint32(buf[:]), nil
}

func (fs *ExFatFS) setFATEntry(c uint32, value uint32) error {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], value)
	if err := fs.writeAt(buf[:], fs.fatEntryOffset(c)); err != nil {
		return fmt.Errorf("vault: write exFAT FAT entry %d: %w", c, err)
	}
	return nil
}

func (fs *ExFatFS) isEOC(v uint32) bool { return v >= 0xFFFFFFF8 }

// chainClusters lists every cluster in the FAT-linked chain starting at
// start, bounded by the volume's own cluster count so a corrupt or cyclic
// FAT cannot spin this loop forever.
func (fs *ExFatFS) chainClusters(start uint32) ([]uint32, error) {
	if start == 0 {
		return nil, nil
	}
	clusters := make([]uint32, 0, 8)
	c := start
	limit, nerr := num.Narrow[int](fs.bpb.clusterCount + 2)
	if nerr != nil {
		return nil, fmt.Errorf("vault: volume cluster count out of range: %w", nerr)
	}
	for {
		clusters = append(clusters, c)
		if len(clusters) > limit {
			return nil, fmt.Errorf("vault: exFAT cluster chain longer than the volume, probably cyclic")
		}
		next, err := fs.getFATEntry(c)
		if err != nil {
			return nil, err
		}
		if fs.isEOC(next) {
			return clusters, nil
		}
		if next < 2 || next == exfatBad || next >= fs.bpb.clusterCount+2 {
			return nil, fmt.Errorf("vault: exFAT cluster chain references invalid cluster %d", next)
		}
		c = next
	}
}

// clusterList resolves a content's cluster list, honoring the stream
// extension's NoFatChain flag: a contiguous extent's clusters are implied
// by its first cluster and size, and its FAT entries are not maintained
// and must not be walked, exactly the trap a reader that always consults
// the FAT would fall into.
func (fs *ExFatFS) clusterList(start uint32, dataLength uint64, noFatChain bool) ([]uint32, error) {
	if start == 0 {
		return nil, nil
	}
	if !noFatChain {
		return fs.chainClusters(start)
	}
	bpc := uint64(fs.bpb.bytesPerCluster())
	count := (dataLength + bpc - 1) / bpc
	if count == 0 {
		count = 1
	}
	n := mustNarrow[uint32](count, "contiguous extent cluster count")
	if uint64(start)+uint64(n) > uint64(fs.bpb.clusterCount)+2 {
		return nil, fmt.Errorf("vault: contiguous extent runs past the volume")
	}
	clusters := make([]uint32, n)
	for i := range clusters {
		clusters[i] = start + uint32(i)
	}
	return clusters, nil
}

// allocateClusters hands out n fresh clusters linked into one FAT chain
// ending in EOC, marking each one allocated in the bitmap: exFAT's real
// allocation record, which this driver keeps in step with the FAT on every
// call rather than trusting the FAT's own zero entries the way FAT32 does.
func (fs *ExFatFS) allocateClusters(n int) ([]uint32, error) {
	if n == 0 {
		return nil, nil
	}
	need, nerr := num.Narrow[uint32](n)
	if nerr != nil {
		return nil, fmt.Errorf("vault: allocate %d exFAT clusters: %w", n, nerr)
	}
	if need > fs.freeCount {
		return nil, ErrNoSpaceOnVolume
	}
	found := make([]uint32, 0, n)
	for c := uint32(2); c < fs.bpb.clusterCount+2 && len(found) < n; c++ {
		allocated, err := fs.bitmapBit(c)
		if err != nil {
			return nil, err
		}
		if !allocated {
			found = append(found, c)
		}
	}
	if len(found) < n {
		return nil, ErrNoSpaceOnVolume
	}
	for i, c := range found {
		value := uint32(exfatEOC)
		if i+1 < len(found) {
			value = found[i+1]
		}
		if err := fs.setFATEntry(c, value); err != nil {
			return nil, err
		}
		if err := fs.setBitmapBit(c, true); err != nil {
			return nil, err
		}
	}
	fs.freeCount -= need
	return found, nil
}

// extendChain allocates n more clusters and links them onto the end of the
// chain whose current last cluster is last.
func (fs *ExFatFS) extendChain(last uint32, n int) ([]uint32, error) {
	added, err := fs.allocateClusters(n)
	if err != nil {
		return nil, err
	}
	if err := fs.setFATEntry(last, added[0]); err != nil {
		return nil, err
	}
	return added, nil
}

// freeChain releases every cluster in a FAT-linked chain: clears its
// bitmap bit and zeroes its FAT entry.
func (fs *ExFatFS) freeChain(start uint32) error {
	if start == 0 {
		return nil
	}
	clusters, err := fs.chainClusters(start)
	if err != nil {
		return err
	}
	for _, c := range clusters {
		if err := fs.setFATEntry(c, 0); err != nil {
			return err
		}
		if err := fs.setBitmapBit(c, false); err != nil {
			return err
		}
	}
	fs.freeCount += mustNarrow[uint32](len(clusters), "freed exFAT cluster count")
	return nil
}

// freeContiguous releases a NoFatChain extent's clusters: only its bitmap
// bits, since a contiguous extent's FAT entries were never this driver's
// to maintain and must not be zeroed as though they linked anything.
func (fs *ExFatFS) freeContiguous(start uint32, dataLength uint64) error {
	clusters, err := fs.clusterList(start, dataLength, true)
	if err != nil {
		return err
	}
	for _, c := range clusters {
		if err := fs.setBitmapBit(c, false); err != nil {
			return err
		}
	}
	fs.freeCount += mustNarrow[uint32](len(clusters), "freed exFAT cluster count")
	return nil
}

// truncateChainAfter keeps the first keep clusters of a FAT-linked chain,
// marks the keep-th one EOC, and frees the rest.
func (fs *ExFatFS) truncateChainAfter(start uint32, keep int) error {
	clusters, err := fs.chainClusters(start)
	if err != nil {
		return err
	}
	if keep >= len(clusters) {
		return nil
	}
	if err := fs.setFATEntry(clusters[keep-1], exfatEOC); err != nil {
		return err
	}
	freed := uint32(0)
	for _, c := range clusters[keep:] {
		if err := fs.setFATEntry(c, 0); err != nil {
			return err
		}
		if err := fs.setBitmapBit(c, false); err != nil {
			return err
		}
		freed++
	}
	fs.freeCount += freed
	return nil
}

// Space reports the exFAT filesystem's own free space.
func (fs *ExFatFS) Space() (total, free uint64) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	bpc := uint64(fs.bpb.bytesPerCluster())
	return uint64(fs.bpb.clusterCount) * bpc, uint64(fs.freeCount) * bpc
}

// Sync flushes nothing beyond what every write already committed: unlike
// FAT32's FSInfo, exFAT keeps no separate free-cluster hint sector this
// driver could fall behind on. It exists to satisfy the same shape fat.go's
// FS.Sync does.
func (fs *ExFatFS) Sync() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return nil
}

// chooseExFatSectorsPerClusterShift scales a fresh exFAT's cluster size to
// the volume size, the same reasoning fat.go's chooseSectorsPerCluster
// documents: a small container gets a small cluster so its allocation
// bitmap can track space finely, and a large one gets a larger cluster so
// the FAT and bitmap stay a small fraction of the volume.
func chooseExFatSectorsPerClusterShift(sizeBytes uint64) uint32 {
	switch {
	case sizeBytes < 256<<20:
		return 3 // 4 KiB
	case sizeBytes < 32<<30:
		return 6 // 32 KiB
	case sizeBytes < 256<<30:
		return 7 // 64 KiB
	default:
		return 8 // 128 KiB
	}
}

// computeExFatFATSize finds the smallest single-FAT size, in sectors, that
// can address every cluster the remaining space yields once that many
// sectors are set aside for it. The relationship is circular in exactly
// the way fat.go's computeFATSize documents, and is solved the same way:
// a closed-form estimate, corrected until the fixed point holds exactly.
func computeExFatFATSize(availSectors uint64, spcShift uint32, bytesPerSector uint32) (fatSectors uint32, totalClusters uint32, err error) {
	bps := uint64(bytesPerSector)
	spc := uint64(1) << spcShift
	entriesPerSector := bps / 4

	numer := 4*availSectors + 8*spc
	denom := bps*spc + 4
	guess := (numer + denom - 1) / denom
	if guess < 1 {
		guess = 1
	}
	for {
		if guess >= availSectors {
			return 0, 0, fmt.Errorf("vault: volume too small to hold an exFAT filesystem")
		}
		dataSectors := availSectors - guess
		clusters := dataSectors / spc
		if clusters > uint64(maxExFatClusterCount) {
			clusters = uint64(maxExFatClusterCount)
		}
		needed := (clusters + entriesPerSector - 1) / entriesPerSector
		if needed <= guess {
			return uint32(guess), uint32(clusters), nil
		}
		guess = needed
	}
}

// encodeExFatBootSector lays out the 512-byte exFAT boot sector Format
// writes, identically for the primary copy at sector 0 and the backup
// copy twelve sectors later.
func encodeExFatBootSector(bpb exfatBPB) []byte {
	sector := make([]byte, 512)
	sector[0], sector[1], sector[2] = 0xEB, 0x76, 0x90
	copy(sector[3:11], "EXFAT   ")
	binary.LittleEndian.PutUint64(sector[72:80], bpb.volumeLength)
	binary.LittleEndian.PutUint32(sector[80:84], bpb.fatOffset)
	binary.LittleEndian.PutUint32(sector[84:88], bpb.fatLength)
	binary.LittleEndian.PutUint32(sector[88:92], bpb.clusterHeapOffset)
	binary.LittleEndian.PutUint32(sector[92:96], bpb.clusterCount)
	binary.LittleEndian.PutUint32(sector[96:100], bpb.rootCluster)
	binary.LittleEndian.PutUint32(sector[100:104], bpb.volumeSerial)
	binary.LittleEndian.PutUint16(sector[104:106], 0x0100) // FileSystemRevision 1.00
	// VolumeFlags stays zero: one FAT, a clean volume, no media failure.
	// Both shifts are single bytes on disk and are bounded well under 255
	// by parseExFatBoot, which is the only way a bpb comes into existence.
	sector[108] = byte(bpb.bytesPerSectorShift & 0xff)
	sector[109] = byte(bpb.sectorsPerClusterShift & 0xff)
	sector[110] = 1 // NumberOfFats
	sector[111] = 0x80
	// PercentInUse stays zero: Format leaves nothing allocated beyond the
	// bitmap, up-case table and root directory this function writes itself.
	sector[510], sector[511] = 0x55, 0xAA
	return sector
}

// encodeExFatExtendedBootSector is the 512-byte template for each of the
// eight extended boot sectors in a boot region: entirely reserved except
// for the boot signature every sector-sized structure in this region ends
// with.
func encodeExFatExtendedBootSector() []byte {
	sector := make([]byte, 512)
	sector[508], sector[509], sector[510], sector[511] = 0x00, 0x00, 0x55, 0xAA
	return sector
}

// writeExFatBootRegion writes one full twelve-sector boot region (the
// primary at sector 0, or the backup at sector exFatBootRegionSectors)
// starting at baseSector: the boot sector, eight extended boot sectors,
// a zeroed OEM parameters sector, a reserved sector, and the checksum
// sector this region's own checksum covers.
func writeExFatBootRegion(dev Device, baseSector uint32, boot []byte, checksum uint32, bps uint32) error {
	off := int64(baseSector) * int64(bps)
	if _, err := dev.WriteAt(boot, off); err != nil {
		return err
	}
	extended := encodeExFatExtendedBootSector()
	for i := int64(1); i <= 8; i++ {
		if _, err := dev.WriteAt(extended, off+i*int64(bps)); err != nil {
			return err
		}
	}
	if err := zeroRegion(dev, off+9*int64(bps), int64(bps)); err != nil { // OEM parameters
		return err
	}
	if err := zeroRegion(dev, off+10*int64(bps), int64(bps)); err != nil { // reserved
		return err
	}
	checksumSector := make([]byte, bps)
	for i := uint32(0); i+4 <= bps; i += 4 {
		binary.LittleEndian.PutUint32(checksumSector[i:i+4], checksum)
	}
	if _, err := dev.WriteAt(checksumSector, off+11*int64(bps)); err != nil {
		return err
	}
	return nil
}

// FormatExFat writes a fresh exFAT filesystem into dev, sized sizeBytes:
// both boot regions, the FAT, the allocation bitmap, the standard up-case
// table, and an empty root directory naming them. It writes no volume
// label entry, which is itself a valid, spec-compliant "no label" state,
// the same one a freshly formatted Windows exFAT volume can be in.
func FormatExFat(dev Device, sizeBytes uint64) error {
	const bps = 512
	const reserved = exFatBootRegionSectors * 2 // primary and backup boot regions
	totalSectors := sizeBytes / bps
	if totalSectors > 0xFFFFFFFF {
		totalSectors = 0xFFFFFFFF
	}
	if totalSectors <= reserved {
		return fmt.Errorf("vault: volume too small to hold an exFAT filesystem")
	}
	spcShift := chooseExFatSectorsPerClusterShift(sizeBytes)
	fatLength, clusterCount, err := computeExFatFATSize(totalSectors-reserved, spcShift, bps)
	if err != nil {
		return err
	}
	clusterHeapOffset := reserved + fatLength

	upcaseRaw, err := base64.StdEncoding.DecodeString(defaultExFatUpcaseTableB64)
	if err != nil {
		return fmt.Errorf("vault: decode embedded up-case table: %w", err)
	}
	bytesPerCluster := uint64(1) << (9 + spcShift)
	bitmapBytes := (uint64(clusterCount) + 7) / 8
	bitmapClusters := (bitmapBytes + bytesPerCluster - 1) / bytesPerCluster
	upcaseClusters := (uint64(len(upcaseRaw)) + bytesPerCluster - 1) / bytesPerCluster
	systemClusters := bitmapClusters + upcaseClusters + 1 // + the root directory's own cluster
	if systemClusters >= uint64(clusterCount) {
		return fmt.Errorf("vault: volume too small to hold an exFAT filesystem")
	}
	bitmapCluster := uint32(2)
	upcaseCluster := mustNarrow[uint32](uint64(bitmapCluster)+bitmapClusters, "up-case table start cluster")
	rootCluster := mustNarrow[uint32](uint64(upcaseCluster)+upcaseClusters, "root directory start cluster")

	var serial [4]byte
	if _, rerr := rand.Read(serial[:]); rerr != nil {
		return fmt.Errorf("vault: generate volume serial: %w", rerr)
	}

	bpb := exfatBPB{
		volumeLength:           totalSectors,
		fatOffset:              reserved,
		fatLength:              fatLength,
		clusterHeapOffset:      clusterHeapOffset,
		clusterCount:           clusterCount,
		rootCluster:            rootCluster,
		volumeSerial:           binary.LittleEndian.Uint32(serial[:]),
		bytesPerSectorShift:    9,
		sectorsPerClusterShift: spcShift,
	}
	fs := &ExFatFS{dev: dev, bpb: bpb, bitmapCluster: bitmapCluster, bitmapBytes: mustNarrow[uint32](bitmapBytes, "allocation bitmap size")}

	boot := encodeExFatBootSector(bpb)
	region := make([]byte, int64(bps)*exFatChecksummedSectors)
	copy(region, boot)
	for i := int64(1); i <= 8; i++ {
		copy(region[i*bps:], encodeExFatExtendedBootSector())
	}
	checksum := bootChecksum(region, bps)
	if err := writeExFatBootRegion(dev, 0, boot, checksum, bps); err != nil {
		return fmt.Errorf("vault: write exFAT boot region: %w", err)
	}
	if err := writeExFatBootRegion(dev, exFatBootRegionSectors, boot, checksum, bps); err != nil {
		return fmt.Errorf("vault: write backup exFAT boot region: %w", err)
	}

	if err := zeroRegion(dev, fs.sectorOffset(reserved), int64(fatLength)*bps); err != nil {
		return fmt.Errorf("vault: clear exFAT FAT: %w", err)
	}
	if err := fs.setFATEntry(0, 0xFFFFFFF8); err != nil {
		return err
	}
	if err := fs.setFATEntry(1, exfatEOC); err != nil {
		return err
	}
	if err := formatChainClusters(fs, bitmapCluster, bitmapClusters); err != nil {
		return err
	}
	if err := formatChainClusters(fs, upcaseCluster, upcaseClusters); err != nil {
		return err
	}
	if err := fs.setFATEntry(rootCluster, exfatEOC); err != nil {
		return err
	}

	if err := zeroRegion(dev, fs.clusterOffset(bitmapCluster), mustNarrow[int64](bitmapClusters, "bitmap cluster count")*mustNarrow[int64](bytesPerCluster, "bytes per cluster")); err != nil {
		return fmt.Errorf("vault: clear allocation bitmap: %w", err)
	}
	for c := bitmapCluster; c < rootCluster+1; c++ {
		if err := fs.setBitmapBit(c, true); err != nil {
			return err
		}
	}

	upcasePadded := make([]byte, upcaseClusters*bytesPerCluster)
	copy(upcasePadded, upcaseRaw)
	if err := fs.writeAt(upcasePadded, fs.clusterOffset(upcaseCluster)); err != nil {
		return fmt.Errorf("vault: write up-case table: %w", err)
	}

	root := make([]byte, bytesPerCluster)
	var bitmapEntry, upcaseEntry exRawEntry
	bitmapEntry[0] = exEntryBitmap
	binary.LittleEndian.PutUint32(bitmapEntry[20:24], bitmapCluster)
	binary.LittleEndian.PutUint64(bitmapEntry[24:32], bitmapBytes)
	upcaseEntry[0] = exEntryUpcase
	binary.LittleEndian.PutUint32(upcaseEntry[4:8], tableChecksum(upcaseRaw))
	binary.LittleEndian.PutUint32(upcaseEntry[20:24], upcaseCluster)
	binary.LittleEndian.PutUint64(upcaseEntry[24:32], uint64(len(upcaseRaw)))
	copy(root[0:32], bitmapEntry[:])
	copy(root[32:64], upcaseEntry[:])
	if err := fs.writeCluster(rootCluster, root); err != nil {
		return fmt.Errorf("vault: write root directory: %w", err)
	}

	fs.freeCount = clusterCount - mustNarrow[uint32](systemClusters, "system cluster count")
	return nil
}

// formatChainClusters links count clusters starting at first into one FAT
// chain ending in EOC, for a system structure (the bitmap, the up-case
// table) Format lays out contiguously.
func formatChainClusters(fs *ExFatFS, first uint32, count uint64) error {
	n := mustNarrow[uint32](count, "system cluster run length")
	for i := range n {
		c := first + i
		value := uint32(exfatEOC)
		if i+1 < n {
			value = c + 1
		}
		if err := fs.setFATEntry(c, value); err != nil {
			return err
		}
	}
	return nil
}
