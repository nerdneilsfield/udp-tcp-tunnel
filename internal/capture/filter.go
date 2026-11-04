package capture

import (
	"errors"
	"fmt"
	"net/netip"

	"golang.org/x/net/bpf"
)

// pktTypeOutgoing is PACKET_OUTGOING: frames this host transmitted.
const pktTypeOutgoing = 4

// FilterRule selects UDP packets for the kernel BPF filter. A packet is
// accepted when it matches any rule. Zero-valued optional fields match
// everything.
type FilterRule struct {
	Src     netip.Addr // optional source host
	SrcPort uint16     // optional source port
	DstPort uint16     // required destination port
}

// CompileFilter builds a classic BPF program, equivalent to
//
//	(udp and dst port P [and src host H] [and src port S]) or ...
//
// for untagged and 802.1Q-tagged IPv4/IPv6 frames, ignoring frames this host
// sent. Non-first IP fragments are rejected in the kernel; first fragments
// pass so the parser can count them. No libpcap is involved.
func CompileFilter(rules []FilterRule) ([]bpf.RawInstruction, error) {
	prog, err := buildFilter(rules, true)
	if err != nil {
		return nil, err
	}
	return bpf.Assemble(prog)
}

// buildFilter returns the program. skipOutgoing adds the packet-type
// check, which only the kernel can evaluate (the userspace VM used in tests
// does not implement it).
func buildFilter(rules []FilterRule, skipOutgoing bool) ([]bpf.Instruction, error) {
	if len(rules) == 0 {
		return nil, errors.New("filter needs at least one rule")
	}
	a := &asm{labels: make(map[string]int)}

	if skipOutgoing {
		a.plain(bpf.LoadExtension{Num: bpf.ExtType})
		a.cond(bpf.JumpEqual, pktTypeOutgoing, "reject", "")
	}
	a.plain(bpf.LoadAbsolute{Off: 12, Size: 2})
	a.cond(bpf.JumpEqual, etherTypeIPv4, "v4_0", "")
	a.cond(bpf.JumpEqual, etherTypeIPv6, "v6_0", "")
	a.cond(bpf.JumpEqual, etherTypeVLAN, "vlan", "reject")

	a.label("vlan")
	a.plain(bpf.LoadAbsolute{Off: 16, Size: 2})
	a.cond(bpf.JumpEqual, etherTypeIPv4, "v4_4", "")
	a.cond(bpf.JumpEqual, etherTypeIPv6, "v6_4", "reject")

	for _, vlan := range []uint32{0, vlanTagLen} {
		a.ipv4(fmt.Sprintf("v4_%d", vlan), vlan, rules)
		a.ipv6(fmt.Sprintf("v6_%d", vlan), vlan, rules)
	}

	a.label("accept")
	a.plain(bpf.RetConstant{Val: 0xffff})
	a.label("reject")
	a.plain(bpf.RetConstant{Val: 0})
	return a.assemble()
}

// ipv4 emits the IPv4 branch; vo is the VLAN tag length in front of IP.
func (a *asm) ipv4(label string, vo uint32, rules []FilterRule) {
	ip := ethHeaderLen + vo // offset of the IP header

	a.label(label)
	a.plain(bpf.LoadAbsolute{Off: ip + 9, Size: 1})
	a.cond(bpf.JumpEqual, protoUDP, "", "reject")
	a.plain(bpf.LoadAbsolute{Off: ip + 6, Size: 2})
	a.cond(bpf.JumpBitsSet, 0x1fff, "reject", "") // non-first fragment
	a.plain(bpf.LoadMemShift{Off: ip})            // X = IP header length

	for _, r := range rules {
		if r.Src.IsValid() && !r.Src.Is4() {
			continue
		}
		next := a.unique()
		a.plain(bpf.LoadIndirect{Off: ip + 2, Size: 2}) // dst port
		a.cond(bpf.JumpEqual, uint32(r.DstPort), "", next)
		if r.SrcPort != 0 {
			a.plain(bpf.LoadIndirect{Off: ip, Size: 2})
			a.cond(bpf.JumpEqual, uint32(r.SrcPort), "", next)
		}
		if r.Src.IsValid() {
			b := r.Src.As4()
			a.plain(bpf.LoadAbsolute{Off: ip + 12, Size: 4})
			a.cond(bpf.JumpEqual, be32(b[:]), "", next)
		}
		a.jump("accept")
		a.label(next)
	}
	a.jump("reject")
}

// ipv6 emits the IPv6 branch. Extension headers are not walked in the
// kernel: such packets are rejected.
func (a *asm) ipv6(label string, vo uint32, rules []FilterRule) {
	ip := ethHeaderLen + vo
	udp := ip + ipv6Header

	a.label(label)
	a.plain(bpf.LoadAbsolute{Off: ip + 6, Size: 1}) // next header
	a.cond(bpf.JumpEqual, protoUDP, "", "reject")

	for _, r := range rules {
		if r.Src.IsValid() && !r.Src.Is6() {
			continue
		}
		next := a.unique()
		a.plain(bpf.LoadAbsolute{Off: udp + 2, Size: 2})
		a.cond(bpf.JumpEqual, uint32(r.DstPort), "", next)
		if r.SrcPort != 0 {
			a.plain(bpf.LoadAbsolute{Off: udp, Size: 2})
			a.cond(bpf.JumpEqual, uint32(r.SrcPort), "", next)
		}
		if r.Src.IsValid() {
			b := r.Src.As16()
			for i := range 4 {
				a.plain(bpf.LoadAbsolute{Off: ip + 8 + uint32(i)*4, Size: 4})
				a.cond(bpf.JumpEqual, be32(b[i*4:]), "", next)
			}
		}
		a.jump("accept")
		a.label(next)
	}
	a.jump("reject")
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// asm is a tiny BPF assembler that resolves symbolic jump targets into the
// relative skips classic BPF requires. Jumps only go forward.
type asm struct {
	ops     []asmOp
	labels  map[string]int
	pending []string
	counter int
}

type asmOp struct {
	ins      bpf.Instruction // used when kind == opPlain
	kind     opKind
	cond     bpf.JumpTest
	val      uint32
	ifTrue   string // empty means fall through
	ifFalse  string
	jumpName string
}

type opKind int

const (
	opPlain opKind = iota
	opCond
	opJump
)

func (a *asm) unique() string {
	a.counter++
	return fmt.Sprintf("next%d", a.counter)
}

func (a *asm) label(name string) { a.pending = append(a.pending, name) }

func (a *asm) add(op asmOp) {
	for _, name := range a.pending {
		a.labels[name] = len(a.ops)
	}
	a.pending = nil
	a.ops = append(a.ops, op)
}

func (a *asm) plain(ins bpf.Instruction) { a.add(asmOp{kind: opPlain, ins: ins}) }

func (a *asm) cond(test bpf.JumpTest, val uint32, ifTrue, ifFalse string) {
	a.add(asmOp{kind: opCond, cond: test, val: val, ifTrue: ifTrue, ifFalse: ifFalse})
}

func (a *asm) jump(name string) { a.add(asmOp{kind: opJump, jumpName: name}) }

func (a *asm) skip(from int, name string) (int, error) {
	if name == "" {
		return 0, nil
	}
	target, ok := a.labels[name]
	if !ok {
		return 0, fmt.Errorf("bpf: undefined label %q", name)
	}
	if target <= from {
		return 0, fmt.Errorf("bpf: backward jump to %q", name)
	}
	return target - from - 1, nil
}

func (a *asm) assemble() ([]bpf.Instruction, error) {
	out := make([]bpf.Instruction, len(a.ops))
	for i, op := range a.ops {
		switch op.kind {
		case opPlain:
			out[i] = op.ins
		case opJump:
			n, err := a.skip(i, op.jumpName)
			if err != nil {
				return nil, err
			}
			out[i] = bpf.Jump{Skip: uint32(n)}
		case opCond:
			t, err := a.skip(i, op.ifTrue)
			if err != nil {
				return nil, err
			}
			f, err := a.skip(i, op.ifFalse)
			if err != nil {
				return nil, err
			}
			if t > 255 || f > 255 {
				return nil, errors.New("bpf: too many capture rules for one filter")
			}
			out[i] = bpf.JumpIf{Cond: op.cond, Val: op.val, SkipTrue: uint8(t), SkipFalse: uint8(f)}
		}
	}
	return out, nil
}
