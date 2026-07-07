// Prukka virtual audio device — ALSA loopback core.
//
// One card with one PCM device: whatever is played into the playback
// substream appears at the capture substream. The microphone module names
// it "Prukka Microphone" (call apps capture the dub the engine plays in);
// the speaker module is the same loopback the other way around (apps play
// the far end, the engine captures it). Identity comes from identity.h in
// the module folder that builds this core.
//
// The format is fixed — 48 kHz, stereo, S16_LE — mirroring the macOS HAL
// driver: one known-good shape instead of a negotiation matrix. An
// hrtimer advances both sides on one clock through a shared frame ring,
// so either side runs alone (capture reads silence with no writer).

#include "identity.h"

#include <linux/hrtimer.h>
#include <linux/module.h>
#include <linux/platform_device.h>
#include <linux/version.h>
#include <linux/vmalloc.h>
#include <sound/core.h>
#include <sound/initval.h>
#include <sound/pcm.h>

#define PRUKKA_RATE 48000
#define PRUKKA_CHANNELS 2
#define PRUKKA_BYTES_PER_FRAME (2 * PRUKKA_CHANNELS)
// ~1.4 s shared ring, a power of two of frames like the macOS driver.
#define PRUKKA_RING_FRAMES 65536
// The timer tick: 10 ms = 480 frames per advance.
#define PRUKKA_TICK_NS (10 * NSEC_PER_MSEC)
#define PRUKKA_TICK_FRAMES (PRUKKA_RATE / 100)

struct prukka_loop {
	struct snd_card *card;
	struct snd_pcm *pcm;
	struct hrtimer timer;
	spinlock_t lock;
	s16 *ring;
	// Ring write head in absolute frames; the reader trails it exactly.
	u64 ring_pos;
	struct snd_pcm_substream *playback;
	struct snd_pcm_substream *capture;
	// Per-side stream state, guarded by lock.
	bool playback_running;
	bool capture_running;
	snd_pcm_uframes_t playback_pos;
	snd_pcm_uframes_t capture_pos;
	unsigned int playback_since_period;
	unsigned int capture_since_period;
};

static const struct snd_pcm_hardware prukka_hw = {
	.info = SNDRV_PCM_INFO_INTERLEAVED | SNDRV_PCM_INFO_MMAP |
		SNDRV_PCM_INFO_MMAP_VALID | SNDRV_PCM_INFO_BLOCK_TRANSFER,
	.formats = SNDRV_PCM_FMTBIT_S16_LE,
	.rates = SNDRV_PCM_RATE_48000,
	.rate_min = PRUKKA_RATE,
	.rate_max = PRUKKA_RATE,
	.channels_min = PRUKKA_CHANNELS,
	.channels_max = PRUKKA_CHANNELS,
	.buffer_bytes_max = 256 * 1024,
	.period_bytes_min = 1024,
	.period_bytes_max = 64 * 1024,
	.periods_min = 2,
	.periods_max = 64,
};

// pump_playback copies one tick of the playback buffer into the ring.
static void pump_playback(struct prukka_loop *loop, unsigned int frames)
{
	struct snd_pcm_runtime *runtime = loop->playback->runtime;
	const s16 *src = (const s16 *)runtime->dma_area;
	unsigned int i;

	for (i = 0; i < frames; i++) {
		u64 slot = (loop->ring_pos + i) % PRUKKA_RING_FRAMES;
		snd_pcm_uframes_t at = loop->playback_pos + i;

		if (at >= runtime->buffer_size)
			at -= runtime->buffer_size;

		memcpy(&loop->ring[slot * PRUKKA_CHANNELS],
		       &src[at * PRUKKA_CHANNELS], PRUKKA_BYTES_PER_FRAME);
	}

	loop->playback_pos += frames;
	if (loop->playback_pos >= runtime->buffer_size)
		loop->playback_pos -= runtime->buffer_size;
}

// pump_capture copies one tick of the ring into the capture buffer; the
// span read is cleared so stale audio never loops (the macOS contract).
static void pump_capture(struct prukka_loop *loop, unsigned int frames)
{
	struct snd_pcm_runtime *runtime = loop->capture->runtime;
	s16 *dst = (s16 *)runtime->dma_area;
	unsigned int i;

	for (i = 0; i < frames; i++) {
		u64 slot = (loop->ring_pos + i) % PRUKKA_RING_FRAMES;
		snd_pcm_uframes_t at = loop->capture_pos + i;

		if (at >= runtime->buffer_size)
			at -= runtime->buffer_size;

		memcpy(&dst[at * PRUKKA_CHANNELS],
		       &loop->ring[slot * PRUKKA_CHANNELS],
		       PRUKKA_BYTES_PER_FRAME);
		memset(&loop->ring[slot * PRUKKA_CHANNELS], 0,
		       PRUKKA_BYTES_PER_FRAME);
	}

	loop->capture_pos += frames;
	if (loop->capture_pos >= runtime->buffer_size)
		loop->capture_pos -= runtime->buffer_size;
}

// tick advances both sides by one timer period on the shared clock.
static enum hrtimer_restart prukka_tick(struct hrtimer *timer)
{
	struct prukka_loop *loop = container_of(timer, struct prukka_loop, timer);
	bool playback_period = false, capture_period = false;
	unsigned long flags;

	spin_lock_irqsave(&loop->lock, flags);

	if (loop->playback_running)
		pump_playback(loop, PRUKKA_TICK_FRAMES);

	if (loop->capture_running)
		pump_capture(loop, PRUKKA_TICK_FRAMES);

	loop->ring_pos += PRUKKA_TICK_FRAMES;

	if (loop->playback_running) {
		loop->playback_since_period += PRUKKA_TICK_FRAMES;
		if (loop->playback_since_period >=
		    loop->playback->runtime->period_size) {
			loop->playback_since_period = 0;
			playback_period = true;
		}
	}

	if (loop->capture_running) {
		loop->capture_since_period += PRUKKA_TICK_FRAMES;
		if (loop->capture_since_period >=
		    loop->capture->runtime->period_size) {
			loop->capture_since_period = 0;
			capture_period = true;
		}
	}

	spin_unlock_irqrestore(&loop->lock, flags);

	// Period callbacks run outside the lock: they may re-enter the ops.
	if (playback_period)
		snd_pcm_period_elapsed(loop->playback);

	if (capture_period)
		snd_pcm_period_elapsed(loop->capture);

	hrtimer_forward_now(timer, ns_to_ktime(PRUKKA_TICK_NS));

	return HRTIMER_RESTART;
}

static int prukka_open(struct snd_pcm_substream *substream)
{
	struct prukka_loop *loop = snd_pcm_substream_chip(substream);
	unsigned long flags;

	substream->runtime->hw = prukka_hw;

	spin_lock_irqsave(&loop->lock, flags);

	if (substream->stream == SNDRV_PCM_STREAM_PLAYBACK)
		loop->playback = substream;
	else
		loop->capture = substream;

	spin_unlock_irqrestore(&loop->lock, flags);

	return 0;
}

static int prukka_close(struct snd_pcm_substream *substream)
{
	struct prukka_loop *loop = snd_pcm_substream_chip(substream);
	unsigned long flags;

	spin_lock_irqsave(&loop->lock, flags);

	if (substream->stream == SNDRV_PCM_STREAM_PLAYBACK) {
		loop->playback = NULL;
		loop->playback_running = false;
	} else {
		loop->capture = NULL;
		loop->capture_running = false;
	}

	spin_unlock_irqrestore(&loop->lock, flags);

	return 0;
}

static int prukka_prepare(struct snd_pcm_substream *substream)
{
	struct prukka_loop *loop = snd_pcm_substream_chip(substream);
	unsigned long flags;

	spin_lock_irqsave(&loop->lock, flags);

	if (substream->stream == SNDRV_PCM_STREAM_PLAYBACK) {
		loop->playback_pos = 0;
		loop->playback_since_period = 0;
	} else {
		loop->capture_pos = 0;
		loop->capture_since_period = 0;
	}

	spin_unlock_irqrestore(&loop->lock, flags);

	return 0;
}

static int prukka_trigger(struct snd_pcm_substream *substream, int cmd)
{
	struct prukka_loop *loop = snd_pcm_substream_chip(substream);
	bool start;
	unsigned long flags;

	switch (cmd) {
	case SNDRV_PCM_TRIGGER_START:
		start = true;
		break;
	case SNDRV_PCM_TRIGGER_STOP:
		start = false;
		break;
	default:
		return -EINVAL;
	}

	spin_lock_irqsave(&loop->lock, flags);

	if (substream->stream == SNDRV_PCM_STREAM_PLAYBACK)
		loop->playback_running = start;
	else
		loop->capture_running = start;

	spin_unlock_irqrestore(&loop->lock, flags);

	return 0;
}

static snd_pcm_uframes_t prukka_pointer(struct snd_pcm_substream *substream)
{
	struct prukka_loop *loop = snd_pcm_substream_chip(substream);
	snd_pcm_uframes_t pos;
	unsigned long flags;

	spin_lock_irqsave(&loop->lock, flags);
	pos = (substream->stream == SNDRV_PCM_STREAM_PLAYBACK)
		      ? loop->playback_pos
		      : loop->capture_pos;
	spin_unlock_irqrestore(&loop->lock, flags);

	return pos;
}

static const struct snd_pcm_ops prukka_ops = {
	.open = prukka_open,
	.close = prukka_close,
	.prepare = prukka_prepare,
	.trigger = prukka_trigger,
	.pointer = prukka_pointer,
};

static struct platform_device *prukka_pdev;
static struct prukka_loop *prukka_state;

static int prukka_probe(struct platform_device *pdev)
{
	struct prukka_loop *loop;
	struct snd_card *card;
	int err;

	err = snd_card_new(&pdev->dev, SNDRV_DEFAULT_IDX1, PRUKKA_CARD_ID,
			   THIS_MODULE, sizeof(*loop), &card);
	if (err < 0)
		return err;

	loop = card->private_data;
	loop->card = card;
	spin_lock_init(&loop->lock);

	loop->ring = vzalloc(PRUKKA_RING_FRAMES * PRUKKA_BYTES_PER_FRAME);
	if (!loop->ring) {
		snd_card_free(card);
		return -ENOMEM;
	}

	err = snd_pcm_new(card, PRUKKA_CARD_ID, 0, 1, 1, &loop->pcm);
	if (err < 0)
		goto fail;

	loop->pcm->private_data = loop;
	strscpy(loop->pcm->name, PRUKKA_CARD_NAME, sizeof(loop->pcm->name));
	snd_pcm_set_ops(loop->pcm, SNDRV_PCM_STREAM_PLAYBACK, &prukka_ops);
	snd_pcm_set_ops(loop->pcm, SNDRV_PCM_STREAM_CAPTURE, &prukka_ops);
	snd_pcm_set_managed_buffer_all(loop->pcm, SNDRV_DMA_TYPE_VMALLOC,
				       NULL, 0, 0);

	strscpy(card->driver, PRUKKA_CARD_ID, sizeof(card->driver));
	strscpy(card->shortname, PRUKKA_CARD_NAME, sizeof(card->shortname));
	strscpy(card->longname, PRUKKA_CARD_NAME " (Prukka)",
		sizeof(card->longname));

	err = snd_card_register(card);
	if (err < 0)
		goto fail;

	// hrtimer_setup() replaced hrtimer_init() in 6.13 and hrtimer_init()
	// was removed in 6.15.
#if LINUX_VERSION_CODE >= KERNEL_VERSION(6, 13, 0)
	hrtimer_setup(&loop->timer, prukka_tick, CLOCK_MONOTONIC,
		      HRTIMER_MODE_REL);
#else
	hrtimer_init(&loop->timer, CLOCK_MONOTONIC, HRTIMER_MODE_REL);
	loop->timer.function = prukka_tick;
#endif
	hrtimer_start(&loop->timer, ns_to_ktime(PRUKKA_TICK_NS),
		      HRTIMER_MODE_REL);

	prukka_state = loop;
	platform_set_drvdata(pdev, loop);

	return 0;

fail:
	vfree(loop->ring);
	snd_card_free(card);

	return err;
}

// The remove callback returned int until 6.7, was void via the .remove_new
// shim from 6.7, and .remove itself became void when .remove_new was
// dropped in 6.13. Match the signature to the kernel being built against.
#if LINUX_VERSION_CODE < KERNEL_VERSION(6, 7, 0)
static int prukka_remove(struct platform_device *pdev)
#else
static void prukka_remove(struct platform_device *pdev)
#endif
{
	struct prukka_loop *loop = platform_get_drvdata(pdev);

	hrtimer_cancel(&loop->timer);
	snd_card_free(loop->card);
	vfree(loop->ring);
	prukka_state = NULL;

#if LINUX_VERSION_CODE < KERNEL_VERSION(6, 7, 0)
	return 0;
#endif
}

static struct platform_driver prukka_driver = {
	.probe = prukka_probe,
#if LINUX_VERSION_CODE < KERNEL_VERSION(6, 7, 0)
	.remove = prukka_remove,
#elif LINUX_VERSION_CODE < KERNEL_VERSION(6, 13, 0)
	.remove_new = prukka_remove,
#else
	.remove = prukka_remove,
#endif
	.driver = { .name = PRUKKA_CARD_ID },
};

static int __init prukka_init(void)
{
	int err;

	err = platform_driver_register(&prukka_driver);
	if (err < 0)
		return err;

	prukka_pdev = platform_device_register_simple(PRUKKA_CARD_ID, -1,
						      NULL, 0);
	if (IS_ERR(prukka_pdev)) {
		platform_driver_unregister(&prukka_driver);
		return PTR_ERR(prukka_pdev);
	}

	return 0;
}

static void __exit prukka_exit(void)
{
	platform_device_unregister(prukka_pdev);
	platform_driver_unregister(&prukka_driver);
}

module_init(prukka_init);
module_exit(prukka_exit);

MODULE_DESCRIPTION(PRUKKA_CARD_NAME " — Prukka virtual audio loopback");
MODULE_AUTHOR("Prukka");
MODULE_LICENSE("GPL");
