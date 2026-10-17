// Panel I/O mocks only. Callback conversion bodies are extracted by firmware.py.
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <iterator>
#include <vector>
#include "PNGdec.h"

#define Log_info(...) ((void)0)
#define BB_MODE_NONE 0
#define BB_MODE_1BPP 1
#define BB_MODE_2BPP 2
#define BB_MODE_4BPP 4
#define BBEP_3COLOR 8
struct Panel {
    int w, h;
    std::vector<uint8_t> scratch, current, previous, written;
    Panel(int width, int height): w(width), h(height), scratch(width*8+32),
        current(width*height), previous(width*height) {}
    int width() { return w; }
    int height() { return h; }
    int capabilities() { return 0; }
    uint8_t *getCache() { return scratch.data(); }
    uint8_t *tempBuffer() { return scratch.data(); }
    uint8_t *currentBuffer() { return current.data(); }
    uint8_t *previousBuffer() { return previous.data(); }
    void setPreviousMode(int) {}
    void writeData(uint8_t *p, int n) { written.insert(written.end(), p, p+n); }
    void drawPixel(int x, int y, int c) { current.at(y*w+x) = c; }
} bbep(8, 1);
uint8_t u8SpectraPal[512];

// EXTRACTED_CALLBACKS

int main(int argc, char **argv) {
    if (argc != 6) return 2;
    std::ifstream file(argv[1], std::ios::binary);
    std::vector<uint8_t> data((std::istreambuf_iterator<char>(file)), {});
    const int mode = atoi(argv[2]), plane = atoi(argv[3]);
    const int w = atoi(argv[4]), h = atoi(argv[5]);
    bbep = Panel(w, h);
    auto png = new PNG(); // Large firmware row buffer must not live on the stack.
    if (mode == 3) {
        printf("%d\n", png_count_colors(png, data.data(), data.size()));
        delete png;
        return 0;
    }
    CreateSpectra6Pal();
    PNG_DRAW_CALLBACK *callback = mode == 0 ? png_draw_og : mode == 1 ? png_draw_fast : png_draw_6clr;
    int user = plane;
    if (png->openRAM(data.data(), data.size(), callback) != PNG_SUCCESS) return 3;
    int rc = png->decode(mode == 0 ? &user : nullptr, 0);
    png->close();
    delete png;
    if (rc != PNG_SUCCESS) return 4;
    auto &result = mode == 0 ? bbep.written : bbep.current;
    fwrite(result.data(), 1, result.size(), stdout);
}
