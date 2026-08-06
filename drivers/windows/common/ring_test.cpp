#include "ring.h"

#include <climits>

static_assert(PrukkaRingReadWindow(0, 0, 8).available == 0, "empty");
static_assert(PrukkaRingReadWindow(8, 0, 8).read == 0, "full");
static_assert(PrukkaRingReadWindow(9, 0, 8).read == 1, "overrun");
static_assert(PrukkaRingReadWindow(20, 4, 8).read == 12, "large overrun");

int main()
{
	const auto normal = PrukkaRingReadWindow(12, 7, 8);
	const auto overrun = PrukkaRingReadWindow(20, 4, 8);
	const auto wrapped = PrukkaRingReadWindow(3, ULLONG_MAX - 2, 4);
	const auto zero = PrukkaRingReadWindow(7, 3, 0);

	return normal.read != 7 || normal.available != 5 ||
	       overrun.read != 12 || overrun.available != 8 ||
	       wrapped.read != ULLONG_MAX || wrapped.available != 4 ||
	       zero.read != 7 || zero.available != 0;
}
