package proc

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	protest "github.com/go-delve/delve/pkg/proc/test"
)

func ptrSizeByRuntimeArch() int {
	return int(unsafe.Sizeof(uintptr(0)))
}

func TestIssue554(t *testing.T) {
	// unsigned integer overflow in proc.(*memCache).contains was
	// causing it to always return true for address 0xffffffffffffffff
	mem := memCache{true, 0x20, make([]byte, 100), nil}
	var addr uint64
	switch ptrSizeByRuntimeArch() {
	case 4:
		addr = 0xffffffff
	case 8:
		addr = 0xffffffffffffffff
	}
	if mem.contains(addr, 40) {
		t.Fatalf("should be false")
	}
}

func TestIssue3760(t *testing.T) {
	// unsigned integer overflow if len(m.cache) < size
	mem := memCache{true, 0x20, make([]byte, 100), nil}
	if mem.contains(0x20, 200) {
		t.Fatalf("should be false")
	}
	// test overflow of end addr
	mem = memCache{true, 0xfffffffffffffff0, make([]byte, 15), nil}
	if !mem.contains(0xfffffffffffffff0, 15) {
		t.Fatalf("should contain it")
	}
	if mem.contains(0xfffffffffffffff0, 16) {
		t.Fatalf("should be false")
	}
	cm := cacheMemory(nil, 0xffffffffffffffff, 1)
	if cm != nil {
		t.Fatalf("should be nil")
	}
}

type dummyMem struct {
	t     *testing.T
	mem   []byte
	base  uint64
	reads []memRead
}

type memRead struct {
	addr uint64
	size int
}

func (dm *dummyMem) ReadMemory(buf []byte, addr uint64) (int, error) {
	dm.t.Logf("read addr=%#x size=%#x\n", addr, len(buf))
	dm.reads = append(dm.reads, memRead{addr, len(buf)})
	a := int64(addr) - int64(dm.base)
	if a < 0 {
		panic("reading below base")
	}
	if int(a)+len(buf) > len(dm.mem) {
		panic("reading beyond end of mem")
	}
	copy(buf, dm.mem[a:])
	return len(buf), nil
}

func (dm *dummyMem) WriteMemory(uint64, []byte) (int, error) {
	panic("not supported")
}

func TestReadCStringValue(t *testing.T) {
	const tgt = "a test string"
	const maxstrlen = 64

	dm := &dummyMem{t: t}
	dm.mem = make([]byte, maxstrlen)
	copy(dm.mem, tgt)

	for _, tc := range []struct {
		base     uint64
		numreads int
	}{
		{0x5000, 1},
		{0x5001, 1},
		{0x4fff, 2},
		{uint64(0x5000 - len(tgt) - 1), 1},
		{uint64(0x5000-len(tgt)-1) + 1, 2},
	} {
		t.Logf("base is %#x\n", tc.base)
		dm.base = tc.base
		dm.reads = dm.reads[:0]
		out, done, err := readCStringValue(dm, tc.base, LoadConfig{MaxStringLen: maxstrlen})
		if err != nil {
			t.Errorf("base=%#x readCStringValue: %v", tc.base, err)
		}
		if !done {
			t.Errorf("base=%#x expected done but wasn't", tc.base)
		}
		if out != tgt {
			t.Errorf("base=%#x got %q expected %q", tc.base, out, tgt)
		}
		if len(dm.reads) != tc.numreads {
			t.Errorf("base=%#x wrong number of reads %d (expected %d)", tc.base, len(dm.reads), tc.numreads)
		}
		if tc.base == 0x4fff && dm.reads[0].size != 1 {
			t.Errorf("base=%#x first read in not of one byte", tc.base)
		}
	}
}

func assertNoError(err error, t testing.TB, s string) {
	if err != nil {
		_, file, line, _ := runtime.Caller(1)
		fname := filepath.Base(file)
		t.Fatalf("failed assertion at %s:%d: %s - %s\n", fname, line, s, err)
	}
}

func TestDwarfVersion(t *testing.T) {
	// Tests that we correctly read the version of compilation units
	fixture := protest.BuildFixture(t, "math", 0)
	bi := NewBinaryInfo(runtime.GOOS, runtime.GOARCH)
	// Use a fake entry point so LoadBinaryInfo does not error in case the binary is PIE.
	const fakeEntryPoint = 1
	assertNoError(bi.LoadBinaryInfo(fixture.Path, fakeEntryPoint, nil), t, "LoadBinaryInfo")
	for _, cu := range bi.Images[0].compileUnits {
		if cu.Version != 4 && cu.Version != 5 {
			t.Errorf("compile unit %q at %#x has bad version %d", cu.name, cu.entry.Offset, cu.Version)
		}
	}
}

func TestRegabiFlagSentinel(t *testing.T) {
	// Detect if the regabi flag in the producer string gets removed
	if !protest.RegabiSupported() {
		t.Skip("irrelevant before Go 1.17 or on non-amd64 architectures")
	}
	fixture := protest.BuildFixture(t, "math", 0)
	bi := NewBinaryInfo(runtime.GOOS, runtime.GOARCH)
	// Use a fake entry point so LoadBinaryInfo does not error in case the binary is PIE.
	const fakeEntryPoint = 1
	assertNoError(bi.LoadBinaryInfo(fixture.Path, fakeEntryPoint, nil), t, "LoadBinaryInfo")
	if !bi.regabi {
		t.Errorf("regabi flag not set %s GOEXPERIMENT=%s", runtime.Version(), os.Getenv("GOEXPERIMENT"))
	}
}

func TestGenericFunctionParser(t *testing.T) {
	// Normal parsing

	var testCases = []struct{ name, pkg, rcv, base string }{
		{"github.com/go-delve/delve.afunc", "github.com/go-delve/delve", "", "afunc"},
		{"github.com/go-delve/delve..afunc", "github.com/go-delve/delve", "", "afunc"}, // malformed
		{"github.com/go-delve/delve.afunc[some/[thing].el se]", "github.com/go-delve/delve", "", "afunc[some/[thing].el se]"},
		{"github.com/go-delve/delve.Receiver.afunc", "github.com/go-delve/delve", "Receiver", "afunc"},
		{"github.com/go-delve/delve.(*Receiver).afunc", "github.com/go-delve/delve", "(*Receiver)", "afunc"},
		{"github.com/go-delve/delve.Receiver.afunc[some/[thing].el se]", "github.com/go-delve/delve", "Receiver", "afunc[some/[thing].el se]"},       // malformed
		{"github.com/go-delve/delve.(*Receiver).afunc[some/[thing].el se]", "github.com/go-delve/delve", "(*Receiver)", "afunc[some/[thing].el se]"}, // malformed
		{"github.com/go-delve/delve.Receiver[some/[thing].el se].afunc", "github.com/go-delve/delve", "Receiver[some/[thing].el se]", "afunc"},
		{"github.com/go-delve/delve.(*Receiver[some/[thing].el se]).afunc", "github.com/go-delve/delve", "(*Receiver[some/[thing].el se])", "afunc"},

		{"github.com/go-delve/delve.afunc[.some/[thing].el se]", "github.com/go-delve/delve", "", "afunc[.some/[thing].el se]"},
		{"github.com/go-delve/delve.Receiver.afunc[.some/[thing].el se]", "github.com/go-delve/delve", "Receiver", "afunc[.some/[thing].el se]"}, // malformed
		{"github.com/go-delve/delve.Receiver[.some/[thing].el se].afunc", "github.com/go-delve/delve", "Receiver[.some/[thing].el se]", "afunc"},
		{"github.com/go-delve/delve.(*Receiver[.some/[thing].el se]).afunc", "github.com/go-delve/delve", "(*Receiver[.some/[thing].el se])", "afunc"},
	}

	for _, tc := range testCases {
		fn := &Function{Name: tc.name}
		if fn.PackageName() != tc.pkg {
			t.Errorf("Package name mismatch: %q %q", tc.pkg, fn.PackageName())
		}
		if fn.ReceiverName() != tc.rcv {
			t.Errorf("Receiver name mismatch: %q %q", tc.rcv, fn.ReceiverName())
		}
		if fn.BaseName() != tc.base {
			t.Errorf("Base name mismatch: %q %q", tc.base, fn.BaseName())
		}
	}
}
