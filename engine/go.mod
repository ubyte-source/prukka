// The engine module is an experimental native speech helper designed for a
// separate bundle (native tools + models), rather than linkage into the daemon.
// This keeps cgo and native-toolchain dependencies out of the daemon module.
// The manual macOS workflow validates an ephemeral build only: it does not
// upload it, releases do not attach it, and setup does not install it.
module github.com/ubyte-source/prukka/engine

go 1.26.5
