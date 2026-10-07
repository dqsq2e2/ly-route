/* Userspace fake-MDIO test; includes only the extracted CPU-link helper. */
#include <assert.h>
#include <errno.h>
#include <stdint.h>
#include <stdbool.h>
#include <stdio.h>
#include <string.h>

typedef uint16_t u16;
#define BIT(n) (1U << (n))
struct mii_bus { int unused; };
struct operation { int addr, reg, value; };
static struct operation writes[32];
static int nwrites, sleeping, status_reads, busy_reads;
static int status, serdes_value, busy_forever, read_failure, write_failure;
static int fail_data, fail_status_after_reset;

static int mdiobus_read(struct mii_bus *bus, int addr, int reg)
{
	(void)bus;
	if (read_failure)
		return -EIO;
	if (addr == 0x1c && reg == 0x18) {
		busy_reads++;
		return busy_forever ? BIT(15) : 0;
	}
	if (addr == 0x1c && reg == 0x19)
		return fail_data ? -EIO : serdes_value;
	assert(addr == 0x14 && reg == 0);
	status_reads++;
	if (fail_status_after_reset && status_reads == 2)
		return -EIO;
	return status;
}

static int mdiobus_write(struct mii_bus *bus, int addr, int reg, u16 value)
{
	(void)bus;
	if (write_failure)
		return -EIO;
	assert(nwrites < 32);
	writes[nwrites++] = (struct operation){addr, reg, value};
	return 0;
}

static void msleep(int ms)
{
	assert(ms == 1000);
	sleeping++;
}

/* The kernel macro bounds elapsed time; this fake uses the equivalent poll count. */
#define read_poll_timeout(op, value, condition, interval, timeout, delay, ...) \
	({ \
		int result = -ETIMEDOUT; \
		(void)(delay); \
		for (int i = 0; i <= (timeout) / (interval); i++) { \
			(value) = op(__VA_ARGS__); \
			if (condition) { result = 0; break; } \
		} \
		result; \
	})

#include "serdes-helper.inc"

static void reset(void)
{
	memset(writes, 0, sizeof(writes));
	nwrites = sleeping = status_reads = busy_reads = 0;
	status = 0x1000;
	serdes_value = 0x1940;
	busy_forever = read_failure = write_failure = 0;
	fail_data = fail_status_after_reset = 0;
}

static void expect_write(int index, int addr, int reg, int value)
{
	assert(writes[index].addr == addr);
	assert(writes[index].reg == reg);
	assert(writes[index].value == value);
}

int main(void)
{
	struct mii_bus bus = {0};

	reset();
	assert(vc_cpu_link_up(&bus) == 0);
	assert(nwrites == 7 && sleeping == 1 && status_reads == 2);
	expect_write(0, 0x1c, 0x19, 1);
	expect_write(1, 0x1c, 0x18, 0x95f6);
	expect_write(2, 0x1c, 0x18, 0x99e0);
	expect_write(3, 0x1c, 0x19, 0x8140);
	expect_write(4, 0x1c, 0x18, 0x95e0);
	expect_write(5, 0x14, 0, 0);
	expect_write(6, 0x14, 1, 0x000e);
	assert(busy_reads == 6);

	reset();
	status = 0x1800;
	assert(vc_cpu_link_up(&bus) == 0);
	assert(nwrites == 2 && sleeping == 0 && busy_reads == 0);
	expect_write(0, 0x14, 0, 0x0800);
	expect_write(1, 0x14, 1, 0x000e);

	reset();
	busy_forever = 1;
	assert(vc_cpu_link_up(&bus) == -ETIMEDOUT);
	assert(nwrites == 0 && sleeping == 0 && busy_reads == 101);

	reset();
	read_failure = 1;
	assert(vc_cpu_link_up(&bus) == -EIO);
	assert(nwrites == 0);

	reset();
	write_failure = 1;
	assert(vc_cpu_link_up(&bus) == -EIO);
	assert(sleeping == 0);

	reset();
	fail_data = 1;
	assert(vc_cpu_link_up(&bus) == -EIO);
	assert(nwrites == 3 && sleeping == 0);

	reset();
	fail_status_after_reset = 1;
	assert(vc_cpu_link_up(&bus) == -EIO);
	assert(nwrites == 5 && sleeping == 1);

	puts("SerDes helper: sequence, already-up, timeout and MDIO error tests passed.");
	return 0;
}
