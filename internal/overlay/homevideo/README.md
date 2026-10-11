# Home Video bitmap font

Home Video 0.90 (Regular) by GGBotNet, CC0 1.0 Universal; see [LICENSE](LICENSE).
The bitmap conversion tools and generated outputs are also CC0 1.0.

Source: [home-video-font-bitmap](https://github.com/dayflower/home-video-font-bitmap)
at commit `6034a71f677f4230e6cccadc427a3a18ff1b3d80`.
The font has 12×18 cells. `homevideo_gen.go` contains only the 13 timestamp
characters plus the upstream missing-glyph asset. Runtime and normal builds
have no external font/tool dependencies.

To regenerate, check out that pinned upstream revision and run from its root:

```sh
uv run hvbitmap go --chars '0123456789-: ' --package homevideo --format raw \
  -o /path/to/arprt/internal/overlay/homevideo/homevideo_gen.go
gofmt -w /path/to/arprt/internal/overlay/homevideo/homevideo_gen.go
```

The independent golden rows in `testdata/homevideo-12x18.txt` are the unchanged
lines for those 13 codepoints from upstream `dist/txt/homevideo-12x18.txt`.
They verify glyph pixels and let cluster smoke checks decode the drawn text
without network access or importing the renderer implementation.
