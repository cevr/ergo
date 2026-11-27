CC ?= gcc
CFLAGS ?= -std=c11 -Wall -Wextra -Werror -O2
ASAN_FLAGS = -fsanitize=address,undefined -g

SRCS = ergo_rc.c ergo_arena.c ergo_actor.c ergo_die.c ergo_io.c
OBJS = $(SRCS:.c=.o)

.PHONY: all test test-asan clean

all: libergort.a

%.o: %.c ergo_rt.h
	$(CC) $(CFLAGS) -c $< -o $@

libergort.a: $(OBJS)
	$(AR) rcs $@ $(OBJS)

test: $(SRCS) ergo_rt_test.c ergo_rt.h
	$(CC) $(CFLAGS) $(SRCS) ergo_rt_test.c -o ergo_rt_test_bin
	./ergo_rt_test_bin --layout
	rm -f ergo_rt_test_bin

test-asan: $(SRCS) ergo_rt_test.c ergo_rt.h
	$(CC) $(CFLAGS) $(ASAN_FLAGS) $(SRCS) ergo_rt_test.c -o ergo_rt_test_asan_bin
	./ergo_rt_test_asan_bin --layout
	rm -f ergo_rt_test_asan_bin

clean:
	rm -f $(OBJS) libergort.a ergo_rt_test_bin ergo_rt_test_asan_bin
