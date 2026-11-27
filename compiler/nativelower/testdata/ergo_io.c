#define _POSIX_C_SOURCE 200809L

#include "ergo_rt.h"

#include <errno.h>
#include <unistd.h>

int64_t ergo_write(int32_t fd, const uint8_t *p, int64_t n) {
    int64_t done = 0;
    while (done < n) {
        ssize_t w = write(fd, p + done, (size_t)(n - done));
        if (w < 0) {
            if (errno == EINTR) {
                continue;
            }
            return done > 0 ? done : -1;
        }
        if (w == 0) {
            break;
        }
        done += (int64_t)w;
    }
    return done;
}
