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

The `video` tag also decides whether **HEIC and AVIF images** render: both are a
video codec (HEVC, AV1) in an image container, so FFmpeg is the decoder either way.
Unlike clips they need no animation setting — a still renders as soon as the tag is
there. PNG, JPEG, GIF, WebP, BMP and TIFF need no tag at all. Note the licence
consequence below: a tag-free release binary shows a phone photo as a paperclip,
which is the trade the release makes deliberately.

`matterbox --version` asks the linked libraries what they are, and prints the build,
its optional features, the platform, and whether this binary would report any
telemetry. Worth pasting into a bug report.

## Distributing a binary you built

Running a tagged build yourself is unrestricted. Giving one to someone else is where the
tags start to matter, because two of them pull in code that matterbox's Apache-2.0
licence can't cover:

- **`-tags demoaudio`** links `github.com/gotracker/opl2` — a GPL-2.0-or-later port of
  DOSBox's OPL synth — by way of the tracker library's core packages. A demoaudio build
  is therefore only distributable under the GPL, never under Apache-2.0. That one is
  unavoidable: it comes from the dependency graph.
- **`-tags video`** links your system's FFmpeg, and what that means depends on how your
  FFmpeg was configured. Plain LGPL FFmpeg ships fine alongside Apache-2.0; one built
  `--enable-gpl` — which most distribution builds are, because it enables x264 and
  friends — does not. So the same commit produces a distributable binary on one machine
  and a non-distributable one on another. `matterbox --version` asks the linked library
  and tells you which you have.

Build tag-free for anything you share. That is what the release binaries are.

```sh
make third-party-licenses    # write the licence bundle for a build
```

It checks both the Go dependency graph and the linked FFmpeg, and refuses to produce a
bundle for a build it can't vouch for.
