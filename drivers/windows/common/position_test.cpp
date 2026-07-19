#include "position.h"

static_assert(PrukkaCyclicPosition(300, 1000, 4000, 4, 1024) == 176,
	      "cyclic position");
static_assert(PrukkaCyclicPosition(1, 0, 4000, 4, 1024) == 0,
	      "invalid frequency");

int main()
{
	PrukkaPositionClock clock = {};

	clock = PrukkaPositionTransition(clock, false, false, 10);
	if (PrukkaPositionElapsed(clock, 90) != 0) {
		return 1;
	}

	clock = PrukkaPositionTransition(clock, true, false, 100);
	clock = PrukkaPositionTransition(clock, true, false, 250);
	if (PrukkaPositionElapsed(clock, 400) != 300) {
		return 2;
	}

	clock = PrukkaPositionTransition(clock, false, false, 400);
	if (PrukkaPositionElapsed(clock, 900) != 300) {
		return 3;
	}

	clock = PrukkaPositionTransition(clock, false, false, 1000);
	clock = PrukkaPositionTransition(clock, true, false, 1200);
	if (PrukkaPositionElapsed(clock, 1300) != 400 ||
	    PrukkaCyclicPosition(PrukkaPositionElapsed(clock, 1300), 1000,
				 4000, 4, 1024) != 576) {
		return 4;
	}

	clock = PrukkaPositionTransition(clock, false, true, 1500);
	return PrukkaPositionElapsed(clock, 2000) != 0;
}
