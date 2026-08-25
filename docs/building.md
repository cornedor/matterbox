# Building

`make install` is all anyone needs. This page is for the two cases it doesn't cover:
unlocking the optional features, and handing a binary you built to someone else.

## Optional features

Some features need C libraries, so they sit behind build tags. `make` detects what your
machine has and compiles in whatever it can — you never have to pick.

```sh
make tags       # what this machine can build, and how to unlock the rest
```

To unlock video, install your distribution's FFmpeg development headers
(`ffmpeg-devel`, `libav*-dev`) and rebuild. Force a specific set with
`make build TAGS=…`, or `TAGS=` for none.

The `video` tag also decides whether **HEIC, AVIF and JPEG XL images** render: each
is a video codec (HEVC, AV1, JXL) in an image container, so FFmpeg is the decoder
either way. Unlike clips they need no animation setting — a still renders as soon as
the tag is there. PNG, JPEG, GIF, WebP, BMP and TIFF need no tag at all.

The release binaries are built with the tag and carry FFmpeg inside them
(`scripts/ffmpeg-static`), so none of this applies to one of those — they decode all
of it out of the box. It only applies to a build of your own.

Two of the three formats need more than the tag, because FFmpeg alone cannot decode
them. **JPEG XL** goes through libjxl (`--enable-libjxl`), and **AVIF** through
libdav1d — FFmpeg's own AV1 decoder only drives hardware, so without dav1d an `.avif`
fails outright. Both are optional external libraries, so the same commit and the same
tag produce a binary that reads them and one that doesn't, depending on how your
distribution built its FFmpeg. Only the linked library knows which you have — ask it:

```sh
matterbox --version    # the "images:" line names what this binary can decode
```

`matterbox --version` asks the linked libraries what they are, and prints the build,
its optional features, the platform, and whether this binary would report any
telemetry. Worth pasting into a bug report.

## Distributing a binary you built

matterbox is GPL-3.0-or-later, so a binary you build is yours to hand out — a tagged
one included. Both optional features link copyleft C code, and both are compatible
with that licence:

- **`-tags demoaudio`** links `github.com/gotracker/opl2`, a GPL-2.0-or-later port of
  DOSBox's OPL synth, by way of the tracker library's core packages.
- **`-tags video`** links your system's FFmpeg. Almost every distribution builds it
  `--enable-gpl` — Fedora, Debian and Homebrew all do — and every GPL configuration
  FFmpeg reports is an "or later" one, so it upgrades to v3. An FFmpeg built without
  `--enable-gpl` is LGPL, which is equally fine.

One build may not be handed to anyone: an FFmpeg configured `--enable-nonfree` is not
redistributable on any terms. `matterbox --version` asks the linked library what it is
and prints the answer, so you never have to guess which one you have.

What travels with the binary is source. The GPL wants whoever you gave it to be able
to get the complete corresponding source of the thing you gave them — the release
binaries are built from a tag, so the public repo covers it, but hand someone a build
from a working tree of your own and they are entitled to that tree.

```sh
make third-party-licenses    # write the licence bundle for a build
```

It reproduces the licence of every Go module in the link, asks a video build's FFmpeg
what it is, and refuses only what cannot be conveyed at all.
