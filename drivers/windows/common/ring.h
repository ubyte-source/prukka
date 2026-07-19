#pragma once

static_assert(sizeof(unsigned long long) == 8, "64-bit ring cursor required");

struct PrukkaRingWindow {
	unsigned long long read;
	unsigned long long available;
};

constexpr PrukkaRingWindow PrukkaRingReadWindow(
	unsigned long long written, unsigned long long read,
	unsigned long long capacity) noexcept
{
	if (capacity == 0) {
		return {written, 0};
	}

	const unsigned long long available = written - read;

	if (available > capacity) {
		return {written - capacity, capacity};
	}

	return {read, available};
}
