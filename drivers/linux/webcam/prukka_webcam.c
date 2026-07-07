// SPDX-License-Identifier: GPL-2.0-only
// Prukka Webcam — a native V4L2 loopback video device.
//
// One /dev/videoN with two faces: the engine writes frames into it (the
// session's video with burned captions, via `prukka session push …
// device://video/N`), and any app that opens it for capture — a browser,
// a call client — sees "Prukka Webcam". The format is fixed at 1280x720
// YUYV like the macOS camera extension: one known-good shape instead of
// a negotiation matrix.
//
// The writer side is plain write(); the capture side is a videobuf2
// vmalloc queue so mmap streaming (what real apps use) works. Capture
// buffers complete at a steady 30 fps regardless of the writer's rhythm:
// the last complete frame is repeated to late readers, and a branded
// idle frame shows before the first write — the device is never black.

#include <linux/module.h>
#include <linux/platform_device.h>
#include <linux/version.h>
#include <linux/vmalloc.h>
#include <linux/workqueue.h>
#include <media/v4l2-device.h>
#include <media/v4l2-ioctl.h>
#include <media/videobuf2-v4l2.h>
#include <media/videobuf2-vmalloc.h>

#define PRUKKA_WIDTH 1280
#define PRUKKA_HEIGHT 720
// YUYV: two bytes per pixel.
#define PRUKKA_FRAME_BYTES (PRUKKA_WIDTH * PRUKKA_HEIGHT * 2)
#define PRUKKA_FPS 30
#define PRUKKA_FRAME_JIFFIES (HZ / PRUKKA_FPS)

struct prukka_cam {
	struct v4l2_device v4l2;
	struct video_device vdev;
	struct vb2_queue queue;
	struct mutex lock;
	spinlock_t buf_lock;
	struct list_head buffers;
	struct delayed_work deliver_work;
	bool removing;
	// frame is the latest complete image; staging accumulates write()
	// bytes and swaps in only once full, so readers never see a torn
	// or partial frame. frame_lock guards both and the swap.
	struct mutex frame_lock;
	u8 *frame;
	u8 *staging;
	size_t staged;
	u64 sequence;
};

struct prukka_buffer {
	struct vb2_v4l2_buffer vb;
	struct list_head list;
};

static bool schedule_delivery(struct prukka_cam *cam, unsigned long delay)
{
	unsigned long flags;
	bool available;

	// The same lock protects the removal gate and every enqueue.
	spin_lock_irqsave(&cam->buf_lock, flags);
	available = !cam->removing;
	if (available)
		schedule_delayed_work(&cam->deliver_work, delay);
	spin_unlock_irqrestore(&cam->buf_lock, flags);

	return available;
}

static void return_buffers(struct prukka_cam *cam,
			   enum vb2_buffer_state state)
{
	struct prukka_buffer *buf, *next;
	unsigned long flags;
	LIST_HEAD(buffers);

	spin_lock_irqsave(&cam->buf_lock, flags);
	list_splice_init(&cam->buffers, &buffers);
	spin_unlock_irqrestore(&cam->buf_lock, flags);

	list_for_each_entry_safe(buf, next, &buffers, list) {
		list_del(&buf->list);
		vb2_buffer_done(&buf->vb.vb2_buf, state);
	}
}

// deliver_frame completes one queued capture buffer with the current
// frame, then re-arms itself one frame interval out: apps see a steady
// 30 fps however fast they queue buffers or the engine writes.
static void deliver_frame(struct work_struct *work)
{
	struct prukka_cam *cam = container_of(to_delayed_work(work),
					      struct prukka_cam, deliver_work);
	struct prukka_buffer *buf;
	unsigned long flags;

	spin_lock_irqsave(&cam->buf_lock, flags);
	buf = list_first_entry_or_null(&cam->buffers, struct prukka_buffer,
				       list);
	if (buf)
		list_del(&buf->list);
	spin_unlock_irqrestore(&cam->buf_lock, flags);

	if (buf) {
		void *dst = vb2_plane_vaddr(&buf->vb.vb2_buf, 0);

		// The 1.8 MB copy runs in process context outside buf_lock,
		// never with interrupts off.
		mutex_lock(&cam->frame_lock);
		memcpy(dst, cam->frame, PRUKKA_FRAME_BYTES);
		mutex_unlock(&cam->frame_lock);

		vb2_set_plane_payload(&buf->vb.vb2_buf, 0, PRUKKA_FRAME_BYTES);
		buf->vb.sequence = cam->sequence++;
		buf->vb.vb2_buf.timestamp = ktime_get_ns();
		vb2_buffer_done(&buf->vb.vb2_buf, VB2_BUF_STATE_DONE);
	}

	schedule_delivery(cam, PRUKKA_FRAME_JIFFIES);
}

// idle_frame paints the branded splash: Prukka blue with a white band —
// visibly alive before the engine's first frame, like the macOS camera.
static void idle_frame(u8 *frame)
{
	// YUYV for the brand blue (#4F8CFF) and for white.
	const u8 blue[4] = { 0x8a, 0xa5, 0x8a, 0x60 };
	const u8 white[4] = { 0xeb, 0x80, 0xeb, 0x80 };
	int row, pair;

	for (row = 0; row < PRUKKA_HEIGHT; row++) {
		bool band = row >= PRUKKA_HEIGHT / 2 - 24 &&
			    row < PRUKKA_HEIGHT / 2 + 24;
		u8 *line = frame + (size_t)row * PRUKKA_WIDTH * 2;

		for (pair = 0; pair < PRUKKA_WIDTH / 2; pair++)
			memcpy(line + pair * 4, band ? white : blue, 4);
	}
}

// MARK: capture side (videobuf2)

static int queue_setup(struct vb2_queue *q, unsigned int *nbuffers,
		       unsigned int *nplanes, unsigned int sizes[],
		       struct device *alloc_devs[])
{
	if (*nplanes)
		return sizes[0] < PRUKKA_FRAME_BYTES ? -EINVAL : 0;

	*nplanes = 1;
	sizes[0] = PRUKKA_FRAME_BYTES;

	return 0;
}

static void buffer_queue(struct vb2_buffer *vb)
{
	struct prukka_cam *cam = vb2_get_drv_priv(vb->vb2_queue);
	struct vb2_v4l2_buffer *vbuf = to_vb2_v4l2_buffer(vb);
	struct prukka_buffer *buf =
		container_of(vbuf, struct prukka_buffer, vb);
	unsigned long flags;

	spin_lock_irqsave(&cam->buf_lock, flags);
	list_add_tail(&buf->list, &cam->buffers);
	spin_unlock_irqrestore(&cam->buf_lock, flags);
}

static int start_streaming(struct vb2_queue *q, unsigned int count)
{
	struct prukka_cam *cam = vb2_get_drv_priv(q);

	cam->sequence = 0;
	if (schedule_delivery(cam, 0))
		return 0;

	return_buffers(cam, VB2_BUF_STATE_QUEUED);

	return -ENODEV;
}

static void stop_streaming(struct vb2_queue *q)
{
	struct prukka_cam *cam = vb2_get_drv_priv(q);

	// cancel_delayed_work_sync copes with the work re-arming itself:
	// on return it is neither pending nor running.
	cancel_delayed_work_sync(&cam->deliver_work);
	return_buffers(cam, VB2_BUF_STATE_ERROR);
}

static const struct vb2_ops prukka_vb2_ops = {
	.queue_setup = queue_setup,
	.buf_queue = buffer_queue,
	.start_streaming = start_streaming,
	.stop_streaming = stop_streaming,
	// No wait_prepare/wait_finish: the vb2 core drops queue->lock around
	// blocking waits itself, and the callbacks no longer exist in 6.13+.
};

// MARK: writer side (the engine)

// prukka_write consumes bytes up to the frame boundary and returns the
// short count; the caller's write loop supplies the rest. A frame only
// becomes visible once complete — a pointer swap, never a torn image.
static ssize_t prukka_write(struct file *file, const char __user *data,
			    size_t count, loff_t *ppos)
{
	struct prukka_cam *cam = video_drvdata(file);
	size_t take;

	if (count == 0)
		return 0;

	mutex_lock(&cam->frame_lock);

	take = min_t(size_t, count, PRUKKA_FRAME_BYTES - cam->staged);
	if (copy_from_user(cam->staging + cam->staged, data, take)) {
		mutex_unlock(&cam->frame_lock);
		return -EFAULT;
	}

	cam->staged += take;
	if (cam->staged == PRUKKA_FRAME_BYTES) {
		swap(cam->frame, cam->staging);
		cam->staged = 0;
	}

	mutex_unlock(&cam->frame_lock);

	return take;
}

// MARK: ioctls

static int querycap(struct file *file, void *priv,
		    struct v4l2_capability *cap)
{
	strscpy(cap->driver, "prukka_webcam", sizeof(cap->driver));
	strscpy(cap->card, "Prukka Webcam", sizeof(cap->card));
	strscpy(cap->bus_info, "platform:prukka_webcam",
		sizeof(cap->bus_info));

	return 0;
}

static void fill_format(struct v4l2_format *f)
{
	f->fmt.pix.width = PRUKKA_WIDTH;
	f->fmt.pix.height = PRUKKA_HEIGHT;
	f->fmt.pix.pixelformat = V4L2_PIX_FMT_YUYV;
	f->fmt.pix.field = V4L2_FIELD_NONE;
	f->fmt.pix.bytesperline = PRUKKA_WIDTH * 2;
	f->fmt.pix.sizeimage = PRUKKA_FRAME_BYTES;
	f->fmt.pix.colorspace = V4L2_COLORSPACE_SRGB;
}

static int enum_fmt(struct file *file, void *priv, struct v4l2_fmtdesc *f)
{
	if (f->index > 0)
		return -EINVAL;

	f->pixelformat = V4L2_PIX_FMT_YUYV;

	return 0;
}

static int get_fmt(struct file *file, void *priv, struct v4l2_format *f)
{
	fill_format(f);

	return 0;
}

static int enum_input(struct file *file, void *priv, struct v4l2_input *inp)
{
	if (inp->index > 0)
		return -EINVAL;

	inp->type = V4L2_INPUT_TYPE_CAMERA;
	strscpy(inp->name, "Prukka", sizeof(inp->name));

	return 0;
}

static int get_input(struct file *file, void *priv, unsigned int *i)
{
	*i = 0;

	return 0;
}

static int set_input(struct file *file, void *priv, unsigned int i)
{
	return i == 0 ? 0 : -EINVAL;
}

static int enum_framesizes(struct file *file, void *priv,
			   struct v4l2_frmsizeenum *fsize)
{
	if (fsize->index > 0 || fsize->pixel_format != V4L2_PIX_FMT_YUYV)
		return -EINVAL;

	fsize->type = V4L2_FRMSIZE_TYPE_DISCRETE;
	fsize->discrete.width = PRUKKA_WIDTH;
	fsize->discrete.height = PRUKKA_HEIGHT;

	return 0;
}

static int enum_frameintervals(struct file *file, void *priv,
			       struct v4l2_frmivalenum *fival)
{
	if (fival->index > 0 || fival->pixel_format != V4L2_PIX_FMT_YUYV ||
	    fival->width != PRUKKA_WIDTH || fival->height != PRUKKA_HEIGHT)
		return -EINVAL;

	fival->type = V4L2_FRMIVAL_TYPE_DISCRETE;
	fival->discrete.numerator = 1;
	fival->discrete.denominator = PRUKKA_FPS;

	return 0;
}

// The rate is fixed: get and set both report the one true cadence.
static int get_parm(struct file *file, void *priv,
		    struct v4l2_streamparm *parm)
{
	if (parm->type != V4L2_BUF_TYPE_VIDEO_CAPTURE)
		return -EINVAL;

	memset(&parm->parm.capture, 0, sizeof(parm->parm.capture));
	parm->parm.capture.capability = V4L2_CAP_TIMEPERFRAME;
	parm->parm.capture.timeperframe.numerator = 1;
	parm->parm.capture.timeperframe.denominator = PRUKKA_FPS;
	parm->parm.capture.readbuffers = 2;

	return 0;
}

static const struct v4l2_ioctl_ops prukka_ioctl_ops = {
	.vidioc_querycap = querycap,
	.vidioc_enum_fmt_vid_cap = enum_fmt,
	.vidioc_g_fmt_vid_cap = get_fmt,
	// The format is fixed: set/try simply confirm it.
	.vidioc_s_fmt_vid_cap = get_fmt,
	.vidioc_try_fmt_vid_cap = get_fmt,
	.vidioc_enum_framesizes = enum_framesizes,
	.vidioc_enum_frameintervals = enum_frameintervals,
	.vidioc_g_parm = get_parm,
	.vidioc_s_parm = get_parm,
	.vidioc_enum_input = enum_input,
	.vidioc_g_input = get_input,
	.vidioc_s_input = set_input,
	.vidioc_reqbufs = vb2_ioctl_reqbufs,
	.vidioc_querybuf = vb2_ioctl_querybuf,
	.vidioc_qbuf = vb2_ioctl_qbuf,
	.vidioc_dqbuf = vb2_ioctl_dqbuf,
	.vidioc_streamon = vb2_ioctl_streamon,
	.vidioc_streamoff = vb2_ioctl_streamoff,
};

static const struct v4l2_file_operations prukka_fops = {
	.owner = THIS_MODULE,
	.open = v4l2_fh_open,
	.release = vb2_fop_release,
	.read = vb2_fop_read,
	.write = prukka_write,
	.poll = vb2_fop_poll,
	.mmap = vb2_fop_mmap,
	.unlocked_ioctl = video_ioctl2,
};

static struct platform_device *prukka_pdev;

// Runs when the last user of the video device goes away — possibly long
// after driver unbind. The V4L2 lifetime rule is that the embedding
// structure must outlive every open handle, so the final cleanup lives
// here, never in remove().
static void prukka_video_release(struct video_device *vdev)
{
	struct prukka_cam *cam = container_of(vdev, struct prukka_cam, vdev);

	v4l2_device_unregister(&cam->v4l2);
	vfree(cam->frame);
	vfree(cam->staging);
	kfree(cam);
}

static int prukka_probe(struct platform_device *pdev)
{
	struct prukka_cam *cam;
	int err;

	// Not devm: cam embeds the video_device, and open handles may outlive
	// the unbind — prukka_video_release frees everything on last close.
	cam = kzalloc(sizeof(*cam), GFP_KERNEL);
	if (!cam)
		return -ENOMEM;

	cam->frame = vmalloc(PRUKKA_FRAME_BYTES);
	cam->staging = vmalloc(PRUKKA_FRAME_BYTES);
	if (!cam->frame || !cam->staging) {
		err = -ENOMEM;
		goto free_frames;
	}

	idle_frame(cam->frame);
	mutex_init(&cam->lock);
	mutex_init(&cam->frame_lock);
	spin_lock_init(&cam->buf_lock);
	INIT_LIST_HEAD(&cam->buffers);
	INIT_DELAYED_WORK(&cam->deliver_work, deliver_frame);

	err = v4l2_device_register(&pdev->dev, &cam->v4l2);
	if (err)
		goto free_frames;

	cam->queue.type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
	cam->queue.io_modes = VB2_MMAP | VB2_USERPTR | VB2_READ;
	cam->queue.drv_priv = cam;
	cam->queue.buf_struct_size = sizeof(struct prukka_buffer);
	cam->queue.ops = &prukka_vb2_ops;
	cam->queue.mem_ops = &vb2_vmalloc_memops;
	cam->queue.timestamp_flags = V4L2_BUF_FLAG_TIMESTAMP_MONOTONIC;
	cam->queue.lock = &cam->lock;

	err = vb2_queue_init(&cam->queue);
	if (err)
		goto unregister;

	cam->vdev.v4l2_dev = &cam->v4l2;
	cam->vdev.fops = &prukka_fops;
	cam->vdev.ioctl_ops = &prukka_ioctl_ops;
	cam->vdev.queue = &cam->queue;
	cam->vdev.lock = &cam->lock;
	cam->vdev.release = prukka_video_release;
	cam->vdev.device_caps = V4L2_CAP_VIDEO_CAPTURE | V4L2_CAP_STREAMING |
				V4L2_CAP_READWRITE;
	strscpy(cam->vdev.name, "Prukka Webcam", sizeof(cam->vdev.name));
	video_set_drvdata(&cam->vdev, cam);

	err = video_register_device(&cam->vdev, VFL_TYPE_VIDEO, -1);
	if (err)
		goto release_queue;

	platform_set_drvdata(pdev, cam);

	return 0;

release_queue:
	vb2_queue_release(&cam->queue);
unregister:
	v4l2_device_unregister(&cam->v4l2);
free_frames:
	vfree(cam->frame);
	vfree(cam->staging);
	kfree(cam);

	return err;
}

// The void-returning .remove_new arrived in 6.3 as the migration path and
// was retired in 6.13, when .remove itself became void. Using int .remove
// below 6.7 and .remove_new from there keeps every kernel in the support
// range on a signature it accepts.
#if LINUX_VERSION_CODE < KERNEL_VERSION(6, 7, 0)
static int prukka_remove(struct platform_device *pdev)
#else
static void prukka_remove(struct platform_device *pdev)
#endif
{
	struct prukka_cam *cam = platform_get_drvdata(pdev);
	unsigned long flags;

	// Block every re-arm before draining. video_unregister_device may run
	// prukka_video_release immediately when no handle is open.
	mutex_lock(&cam->lock);
	spin_lock_irqsave(&cam->buf_lock, flags);
	cam->removing = true;
	spin_unlock_irqrestore(&cam->buf_lock, flags);
	cancel_delayed_work_sync(&cam->deliver_work);
	// No frame can complete now; wake blocked readers with EIO.
	vb2_queue_error(&cam->queue);
	platform_set_drvdata(pdev, NULL);
	mutex_unlock(&cam->lock);
	video_unregister_device(&cam->vdev);

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
	.driver = { .name = "prukka_webcam" },
};

static int __init prukka_init(void)
{
	int err;

	err = platform_driver_register(&prukka_driver);
	if (err < 0)
		return err;

	prukka_pdev = platform_device_register_simple("prukka_webcam", -1,
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

MODULE_DESCRIPTION("Prukka Webcam — native virtual camera");
MODULE_AUTHOR("Prukka");
MODULE_LICENSE("GPL");
