# The syscall corpus

The images the worker is run against to measure its seccomp allow-list.

The list is produced by running the worker under `SECCOMP_RET_LOG` and reading
the audit log, never by guessing, and it is committed with the corpus that
produced it. That is the whole point of this directory: a reader can rerun the
measurement and get the same answer.

## Regenerating

```sh
cd go
sudo go run ./internal/preview/worker/audit -corpus internal/preview/worker/testdata/corpus
```

Root is needed to read the kernel audit log, which is where `SECCOMP_RET_LOG`
writes. The command prints every syscall the current allow-list is missing, and
prints nothing when the list is complete.

## What is here and why

Generated rather than downloaded, so the corpus is reproducible and carries no
third-party licence. What matters for a syscall measurement is which code paths
run, not whether the pictures are interesting:

| File | Exercises |
|---|---|
| `small.png` | the PNG decoder, RGBA, the common case |
| `large.png` | a decode big enough to move the allocator past its first arena |
| `gray.png` | a different colour model, which is a different pixel path |
| `photo.jpeg` | the JPEG decoder, YCbCr 4:2:0 |
| `progressive.jpeg` | the progressive path, which buffers differently |
| `exif.jpeg` | the EXIF parser and the orientation rotation |
| `anim.gif` | the GIF decoder, one frame of an animation |
| `paletted.gif` | a palette, which is the other GIF pixel path |
| `image.bmp` | the BMP decoder |
| `image.tiff` | the TIFF decoder |
| `truncated.png` | a decoder failing partway, which is its own path |
| `garbage.bin` | the sniffer refusing before any decoder runs |

The failure cases are here deliberately. An error path allocates and unwinds
differently from a success, and a filter measured only against files that
decode is a filter that kills the first time a user uploads a corrupt one.
