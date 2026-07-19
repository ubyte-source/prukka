/* SPDX-License-Identifier: GPL-2.0-only */
/*
 * Shared platform-driver glue for the Prukka Linux modules. Each module is one
 * driver bound to one self-registered platform device, so the driver record,
 * the module init/exit pair and the kernel-version dance around the remove
 * callback are identical in all of them and live here once.
 */
#ifndef PRUKKA_PLATFORM_MODULE_H
#define PRUKKA_PLATFORM_MODULE_H

#include <linux/err.h>
#include <linux/init.h>
#include <linux/platform_device.h>
#include <linux/version.h>

/*
 * The void-returning .remove_new arrived in 6.3 as the migration path and was
 * retired in 6.13, when .remove itself became void. Using int .remove below
 * 6.7 and .remove_new from there keeps every kernel in the support range on a
 * signature it accepts.
 */
#if LINUX_VERSION_CODE < KERNEL_VERSION(6, 7, 0)
#define PRUKKA_REMOVE_TYPE int
#define PRUKKA_REMOVE_RESULT 0
#define PRUKKA_REMOVE_OP remove
#elif LINUX_VERSION_CODE < KERNEL_VERSION(6, 13, 0)
#define PRUKKA_REMOVE_TYPE void
#define PRUKKA_REMOVE_RESULT
#define PRUKKA_REMOVE_OP remove_new
#else
#define PRUKKA_REMOVE_TYPE void
#define PRUKKA_REMOVE_RESULT
#define PRUKKA_REMOVE_OP remove
#endif

/* Closes a remove callback declared with PRUKKA_REMOVE_TYPE. */
#define PRUKKA_REMOVE_DONE return PRUKKA_REMOVE_RESULT

/*
 * PRUKKA_PLATFORM_MODULE registers the driver plus its single device under id,
 * and unregisters both on unload. The parameter cannot be called name: it would
 * capture the .name designator below.
 */
#define PRUKKA_PLATFORM_MODULE(id, probe_fn, remove_fn) \
	static struct platform_device *prukka_pdev; \
	static struct platform_driver prukka_driver = { \
		.probe = probe_fn, \
		.PRUKKA_REMOVE_OP = remove_fn, \
		.driver = { .name = id }, \
	}; \
	static int __init prukka_init(void) \
	{ \
		int err = platform_driver_register(&prukka_driver); \
		if (err < 0) \
			return err; \
		prukka_pdev = platform_device_register_simple(id, -1, NULL, 0); \
		if (IS_ERR(prukka_pdev)) { \
			platform_driver_unregister(&prukka_driver); \
			return PTR_ERR(prukka_pdev); \
		} \
		return 0; \
	} \
	static void __exit prukka_exit(void) \
	{ \
		platform_device_unregister(prukka_pdev); \
		platform_driver_unregister(&prukka_driver); \
	} \
	module_init(prukka_init); \
	module_exit(prukka_exit)

#endif /* PRUKKA_PLATFORM_MODULE_H */
