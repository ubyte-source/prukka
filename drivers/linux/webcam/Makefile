# Build against the running kernel's headers; native toolchain only.
KDIR ?= /lib/modules/$(shell uname -r)/build

all: check
	$(MAKE) -C $(KDIR) M=$(CURDIR) modules

check:
	sh lifecycle-check.sh

clean:
	$(MAKE) -C $(KDIR) M=$(CURDIR) clean

.PHONY: all check clean
