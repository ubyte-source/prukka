#!/bin/sh
set -eu

if [ "$#" -eq 0 ]; then
	src=$(dirname "$0")/prukka_webcam.c
else
	src=$1
fi

fail()
{
	echo "lifecycle-check: $*" >&2
	exit 1
}

body()
{
	awk -v name="$1" '
		$0 ~ "^static .* " name "\\(" { found = 1 }
		found { print }
		found && /^}/ { exit }
	' "$src"
}

line_of()
{
	printf '%s\n' "$1" | awk -v needle="$2" '
		index($0, needle) { print NR; exit }
	'
}

before()
{
	left=$(line_of "$1" "$2")
	right=$(line_of "$1" "$3")
	[ -n "$left" ] || fail "missing: $2"
	[ -n "$right" ] || fail "missing: $3"
	[ "$left" -lt "$right" ] || fail "required order: $2 before $3"
}

schedule=$(body schedule_delivery)
remove=$(body prukka_remove)
[ -n "$schedule" ] || fail "schedule_delivery not found"
[ -n "$remove" ] || fail "prukka_remove not found"

before "$schedule" "spin_lock_irqsave" "available = !cam->removing;"
before "$schedule" "available = !cam->removing;" "schedule_delayed_work"
before "$schedule" "schedule_delayed_work" "spin_unlock_irqrestore"

before "$remove" "mutex_lock(&cam->lock);" "spin_lock_irqsave"
before "$remove" "spin_lock_irqsave" "cam->removing = true;"
before "$remove" "cam->removing = true;" "spin_unlock_irqrestore"
before "$remove" "spin_unlock_irqrestore" "cancel_delayed_work_sync"
before "$remove" "cancel_delayed_work_sync" "vb2_queue_error"
before "$remove" "vb2_queue_error" "mutex_unlock(&cam->lock);"
before "$remove" "mutex_unlock(&cam->lock);" "video_unregister_device(&cam->vdev);"

enqueues=$(grep -Ec \
	'^[[:space:]]*(schedule|queue|mod)_delayed_work(_on)?\([^;]*&cam->deliver_work' \
	"$src" || true)
[ "$enqueues" -eq 1 ] || fail "all delivery enqueues must pass schedule_delivery"

unregisters=$(grep -Fc 'video_unregister_device(&cam->vdev);' "$src" || true)
[ "$unregisters" -eq 1 ] || fail "video device must have one audited unregister path"
