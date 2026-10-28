# angry-mm-generator

A stupid little Go tool: an HTTP endpoint that overlays your message on the
angry emoji, classic meme style.

## Run

```sh
go build .
./angry-mm-generator --addr :8080 --image assets/base.jpg
```

Then:

```sh
curl -G -o meme.png --data-urlencode "message=PATRICK" http://localhost:8080/image
curl -G -o meme.png --data-urlencode "message=ANGRY<br/>MM" http://localhost:8080/image
curl -G -o meme.png --data-urlencode "message=PATRICK" --data-urlencode "grain=10" http://localhost:8080/image
curl -G -o meme.png --data-urlencode "message=PATRICK" --data-urlencode "invert" http://localhost:8080/image
```

## Container image

Pushes to `main` are built with [ko](https://ko.build) by GitHub Actions
(`.github/workflows/publish.yml`) and published to this repo's GHCR:

```sh
docker run --rm -p 8080:8080 ghcr.io/patrickpichler/angry-m-m-gen:latest
```

Tags: `latest` plus `sha-<short sha>` per commit; multi-arch
(`linux/amd64` + `linux/arm64`). The image is self-contained — font and
base image are embedded — so no volumes or extra flags are needed.

## Endpoint

`GET /image?message=<text>` → PNG.

The message is uppercased and rendered in Impact — white fill, black outline,
centered horizontally, pinned near the bottom edge. `<br/>` (or `<br>`, any
casing) starts a new line; the whole block shrinks so its widest line fits,
and lines that still overflow wrap further (block capped at 5 lines, empty
lines from consecutive breaks are dropped). `grain=<0-100>` degrades the
base image with real JPEG re-compression artifacts before the text is
drawn — the caption stays crisp. Higher values mean lower JPEG quality:
subtle blocking around ~10, clearly crunchy around ~40, full repost-mush
at 100; invalid or out-of-range values return 400. Missing/empty
`message` returns 400.

## Flags

Parsed with [cobra](https://github.com/spf13/cobra) — run with `--help` for
the full list (cobra also adds a `completion` subcommand for shell
completions).

| flag | shorthand | default | description |
|--------|-----|-----------------|------------------------|
| `--addr` | `-a` | `:8080` | listen address |
| `--image` | `-i` | `assets/base.jpg` | path to the base image |

## Notes

- The Impact font is embedded from `assets/Impact.ttf` (copied from macOS
  system fonts). Impact is a Microsoft-licensed typeface — fine for personal
  and joke use, but if you distribute this somewhere serious, swap in an
  OFL-licensed lookalike such as [Anton](https://fonts.google.com/specimen/Anton)
  and update the `//go:embed` path in `main.go`.
- Output is PNG. For smaller JPEG responses, swap `png.Encode` for
  `jpeg.Encode` in `imageHandler`.
