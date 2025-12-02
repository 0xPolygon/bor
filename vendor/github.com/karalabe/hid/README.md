[![AppVeyor][appveyorimg]][appveyorurl]
[![GoDoc][docimg]][docurl]

[travisimg]:   https://travis-ci.org/karalabe/hid.svg?branch=master
[travisurl]:   https://travis-ci.org/karalabe/hid
[appveyorimg]: https://ci.appveyor.com/api/projects/status/7cm4ko18etdvkb7f?svg=true
[appveyorurl]: https://ci.appveyor.com/project/karalabe/hid
[docimg]:      https://godoc.org/github.com/karalabe/hid?status.svg
[docurl]:      https://godoc.org/github.com/karalabe/hid

# Gopher Interface Devices (USB HID)

The `hid` package is a cross platform library for accessing and communicating with USB Human Interface
Devices (HID). It is an alternative package to [`gousb`](https://github.com/karalabe/gousb) for use
cases where devices support this ligher mode of operation (e.g. input devices, hardware crypto wallets).

The package wraps [`hidapi`](https://github.com/libusb/hidapi) for accessing OS specific USB HID APIs
directly instead of using low level USB constructs, which might have permission issues on some platforms.
The `hidapi` dependency is vendored directly into the repository and wrapped using CGO, making the `hid` package self-contained
and go-gettable.

Supported platforms at the moment are Linux, macOS and Windows (exclude constraints are also specified
for Android and iOS to allow smoother vendoring into cross platform projects).

The `hidapi` on linux unfortunately requires `libuddev` (`libudev-dev` on ubuntu, `systemd-devel` on fedora). Therefore, 
this library includes [`libusb`](https://github.com/libusb/libusb) which we use as a backend on `linux`, and thus avoid 
runtime dependencies.


## Cross-compiling

Using `go get` the embedded C library is compiled into the binary format of your host OS. Cross compiling to a different platform or architecture entails disabling CGO by default in Go, causing device enumeration `hid.Enumerate()` to yield no results.

To cross compile a functional version of this library, you'll need to enable CGO during cross compilation via `CGO_ENABLED=1` and you'll need to install and set a cross compilation enabled C toolkit via `CC=your-cross-gcc`.

## Acknowledgements

Although the `hid` package is an implementation from scratch, it was heavily inspired by the existing
[`go.hid`](https://github.com/GeertJohan/go.hid) library, which seems abandoned since 2015; is incompatible
with Go 1.6+; and has various external dependencies. Given its inspirational roots, I thought it important
to give credit to the author of said package too.

Wide character support in the `hid` package is done via the [`gowchar`](https://github.com/orofarne/gowchar)
library, unmaintained since 2013; non buildable with a modern Go release and failing `go vet` checks. As
such, `gowchar` was also vendored in inline (copyright headers and origins preserved).

## License

The components of `hid` are licensed as such:

 * `hidapi` is released under the [three licenses](https://github.com/libusb/hidapi/blob/master/LICENSE.txt): [GPL-V3](https://github.com/libusb/hidapi/blob/master/LICENSE-gpl3.txt), [3-clause BSD](https://github.com/libusb/hidapi/blob/master/LICENSE-bsd.txt) and [original HIDAPI](https://github.com/libusb/hidapi/blob/master/LICENSE-orig.txt) license.
 * `libusb` is released under [GPLv2.1](https://github.com/libusb/libusb/blob/master/COPYING)
 * `go.hid` was released under the [2-clause BSD](https://github.com/GeertJohan/go.hid/blob/master/LICENSE) license.
 * `gowchar` is released under the [3-clause BSD](https://github.com/orofarne/gowchar/blob/master/LICENSE) license.

Given the above, `hid` is licensed under GNU LGPL 2.1 or later on Linux and 3-clause BSD on other platforms.
