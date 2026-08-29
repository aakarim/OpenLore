#include <ghostty/vt.h>

#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static void fail(const char *message) {
    fprintf(stderr, "libghostty test: %s\n", message);
    exit(1);
}

static uint8_t *read_file(const char *path, size_t *length) {
    FILE *file = fopen(path, "rb");
    if (file == NULL) fail("could not open transcript");
    if (fseek(file, 0, SEEK_END) != 0) fail("could not seek transcript");
    long size = ftell(file);
    if (size < 0 || fseek(file, 0, SEEK_SET) != 0) fail("could not size transcript");
    uint8_t *data = malloc((size_t)size);
    if (data == NULL) fail("could not allocate transcript");
    if (fread(data, 1, (size_t)size, file) != (size_t)size) fail("could not read transcript");
    fclose(file);
    *length = (size_t)size;
    return data;
}

static bool contains(const uint8_t *data, size_t length, const char *needle) {
    size_t needle_length = strlen(needle);
    if (needle_length > length) return false;
    for (size_t i = 0; i + needle_length <= length; i++) {
        if (memcmp(data + i, needle, needle_length) == 0) return true;
    }
    return false;
}

static void verify(const char *path, uint16_t columns, bool resize) {
    size_t input_length = 0;
    uint8_t *input = read_file(path, &input_length);

    GhosttyTerminal terminal = NULL;
    if (ghostty_terminal_new(NULL, &terminal, 80, 24) != GHOSTTY_SUCCESS) {
        fail("could not create terminal");
    }
    if (resize && ghostty_terminal_resize(terminal, columns, 24, 0, 0) != GHOSTTY_SUCCESS) {
        fail("could not resize terminal");
    }
    ghostty_terminal_vt_write(terminal, input, input_length);
    free(input);

    GhosttyFormatterTerminalOptions options = GHOSTTY_INIT_SIZED(GhosttyFormatterTerminalOptions);
    options.emit = GHOSTTY_FORMATTER_FORMAT_PLAIN;
    options.trim = true;
    GhosttyFormatter formatter = NULL;
    if (ghostty_formatter_terminal_new(NULL, &formatter, terminal, options) != GHOSTTY_SUCCESS) {
        fail("could not create formatter");
    }
    uint8_t *screen = NULL;
    size_t screen_length = 0;
    if (ghostty_formatter_format_alloc(formatter, NULL, &screen, &screen_length) != GHOSTTY_SUCCESS) {
        fail("could not format terminal");
    }

    const char *prompt = resize ? "lore:/ $ cat /docs/n" : "lore:/ $ cat /docs/note";
    if (!contains(screen, screen_length, "notes.md") ||
        !contains(screen, screen_length, "notebook.md") ||
        !contains(screen, screen_length, prompt)) {
        fwrite(screen, 1, screen_length, stderr);
        fail("completion listing or redrawn prompt missing from terminal screen");
    }

    ghostty_free(NULL, screen, screen_length);
    ghostty_formatter_free(formatter);
    ghostty_terminal_free(terminal);
}

int main(int argc, char **argv) {
    if (argc != 3) fail("usage: harness <80-column transcript> <20-column transcript>");
    verify(argv[1], 80, false);
    verify(argv[2], 20, true);
    puts("libghostty terminal rendering: PASS");
    return 0;
}
