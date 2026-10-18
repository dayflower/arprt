# Image processing investigation

These experiments inform the image-processing design in
[DESIGN](../../notes/DESIGN.md#image-processing).
They do not change the controller, establish physical support, or replace the
production software and deployment checks in
[DEVELOP](../../notes/DEVELOP.md#integrated-image-acceptance).

Run from the repository root with Python 3, a C++ compiler and the Go toolchain
selected by the Makefile. Firmware downloads are public pinned sources, verified
against `sources.json`; all generated/downloaded files stay in the supplied
temporary directory. No device or cluster access is used.

```sh
python3 tools/image-investigation/firmware.py --work /tmp/arprt-firmware
GOTOOLCHAIN=go1.26.8 go test -v ./tools/image-investigation
CGO_ENABLED=0 GOTOOLCHAIN=go1.26.8 go build -o /tmp/arprt-image-probe ./tools/image-investigation
/tmp/arprt-image-probe -mode fixtures -dir /tmp/arprt-image-fixtures
/tmp/arprt-image-probe -mode measure -dir /tmp/arprt-image-fixtures
```

The firmware harness compiles extracted original callback bodies against actual
PNGdec 1.1.6, using panel-memory mocks and each target's full-resolution row
buffer setting. It asserts bytes/pixels, including known failures; a passing
experiment does not mean every tested input is device compatible. Downloaded
source retains its upstream license; generated extraction is not vendored into
the controller.
The driver also preprocesses the reviewed model macros to assert literal Model
values and the distribution-specific PNG budgets, including Seeed's mixed-case
model aliases.

The Go probe generates legal PNG combinations and both APNG layouts without an
external imaging library. The measured pipeline is an experimental stretch /
bilinear / Lab / row-error implementation, separate from the production
implementation. It processes the complete output rectangle and uses a synthetic
palette overlay; production contain layout and overlay drawing need their own
integration checks.
Fixture generation is deliberately separate from measurement to avoid counting
generation memory as processing memory. Each measurement should run in a fresh
process. See DESIGN for the exact resource envelope, results and limitations.

With a local controller image available, the resource driver runs each case in
a fresh container with 500m CPU, 512 MiB memory, no swap, a read-only root and no
external network. Its HTTP load case uses loopback inside the container and
eight clients plus one completed Job file, all behind the same admission lease.
It uses a 384 MiB Go GC budget. The recorded
[amd64 measurements](measurements-linux-amd64.json) include raw outputs and the
probe binary hash; times vary by CPU and are not support claims.

```sh
python3 tools/image-investigation/measure.py \
  --binary /tmp/arprt-image-probe --fixtures /tmp/arprt-image-fixtures \
  --image arprt:dev --output /tmp/arprt-image-measurements.json
```

The generated measurement PNG is static indexed/4-bit for all palettes; it
measures packing/compression cost and exact pixels, not the final native-gray
output contract. The probe deliberately reports oversized outputs instead of
altering dithering to fit a budget. Production must reject those outputs.
Cancellation measurements wait for the synchronous operation to finish; there
is no detached worker continuing after a timeout response. The Go decoder's
row/pass work between Reader calls requires adversarial and native-architecture
latency checks when changing the decoder/toolchain, as DESIGN records.
