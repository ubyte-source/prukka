#pragma once

static_assert(sizeof(unsigned long long) == 8,
	      "64-bit position clock required");

struct PrukkaPositionClock {
	unsigned long long elapsedTicks;
	unsigned long long anchorTicks;
	bool running;
};

constexpr PrukkaPositionClock PrukkaPositionTransition(
	PrukkaPositionClock clock, bool running, bool stopped,
	unsigned long long now) noexcept
{
	if (clock.running && !running) {
		clock.elapsedTicks += now - clock.anchorTicks;
	}

	if (stopped) {
		clock.elapsedTicks = 0;
	}

	if (running && !clock.running) {
		clock.anchorTicks = now;
	}

	clock.running = running;

	return clock;
}

constexpr unsigned long long PrukkaPositionElapsed(
	PrukkaPositionClock clock, unsigned long long now) noexcept
{
	return clock.elapsedTicks +
	       (clock.running ? now - clock.anchorTicks : 0);
}

constexpr unsigned long long PrukkaCyclicPosition(
	unsigned long long elapsedTicks, unsigned long long frequency,
	unsigned long long bytesPerSecond, unsigned long long blockAlign,
	unsigned long long bufferSize) noexcept
{
	if (frequency == 0 || blockAlign == 0 || bufferSize == 0) {
		return 0;
	}

	const unsigned long long seconds = elapsedTicks / frequency;
	const unsigned long long remainder = elapsedTicks % frequency;
	const unsigned long long whole =
		((seconds % bufferSize) * bytesPerSecond) % bufferSize;
	const unsigned long long partial =
		(remainder * bytesPerSecond) / frequency;
	const unsigned long long offset =
		(whole + (partial % bufferSize)) % bufferSize;

	return offset - (offset % blockAlign);
}
