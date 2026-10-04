//go:build linux

package egress

import (
	"encoding/binary"
	"net/netip"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

func addressMatch(prefix netip.Prefix) []expr.Any {
	family := byte(unix.NFPROTO_IPV6)
	offset := uint32(24)
	length := uint32(16)
	if prefix.Addr().Is4() {
		family = unix.NFPROTO_IPV4
		offset = 16
		length = 4
	}
	value := prefix.Masked().Addr().AsSlice()
	mask := make([]byte, len(value))
	for bit := 0; bit < prefix.Bits(); bit++ {
		mask[bit/8] |= 1 << uint(7-bit%8)
	}
	return []expr.Any{&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{family}}, &expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: length}, &expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: length, Mask: mask, Xor: make([]byte, length)}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: value}}
}
func destinationPort(proto byte, port uint16) []expr.Any {
	bytes := make([]byte, 2)
	binary.BigEndian.PutUint16(bytes, port)
	return []expr.Any{&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}}, &expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: bytes}}
}
func Install(p Policy) error {
	connection := &nftables.Conn{}
	// Replace only our own table atomically, never flush a shared namespace.
	tables, err := connection.ListTables()
	if err != nil {
		return ErrBoundary
	}
	for _, table := range tables {
		if table.Family == nftables.TableFamilyINet && table.Name == "one_nvr_media" {
			connection.DelTable(table)
		}
	}
	table := connection.AddTable(&nftables.Table{Name: "one_nvr_media", Family: nftables.TableFamilyINet})
	policy := nftables.ChainPolicyDrop
	chain := connection.AddChain(&nftables.Chain{Name: "output", Table: table, Type: nftables.ChainTypeFilter, Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityFilter, Policy: &policy})
	add := func(expressions []expr.Any, verdict expr.VerdictKind) {
		expressions = append(expressions, &expr.Counter{}, &expr.Verdict{Kind: verdict})
		connection.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: expressions})
	}
	state := binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED)
	add([]expr.Any{&expr.Ct{Key: expr.CtKeySTATE, Register: 1}, &expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: state, Xor: make([]byte, 4)}, &expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: make([]byte, 4)}}, expr.VerdictAccept)
	for _, protocol := range []byte{unix.IPPROTO_TCP, unix.IPPROTO_UDP} {
		expressions := addressMatch(netip.MustParsePrefix("127.0.0.11/32"))
		// Docker DNATs its embedded resolver to an ephemeral listener port
		// before this filter chain. Match the original destination port,
		// never allow arbitrary new connections to the loopback address.
		expressions = append(expressions,
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{protocol}},
			&expr.Ct{Key: expr.CtKeyPROTODST, Register: 1, Direction: 0},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0, 53}},
		)
		add(expressions, expr.VerdictAccept)
	}
	for _, ip := range p.HookIPs {
		expressions := addressMatch(netip.PrefixFrom(ip, ip.BitLen()))
		expressions = append(expressions, destinationPort(unix.IPPROTO_TCP, 8083)...)
		add(expressions, expr.VerdictAccept)
	}
	for _, prefix := range special {
		add(addressMatch(prefix), expr.VerdictDrop)
	}
	for _, ip := range p.Denied {
		add(addressMatch(netip.PrefixFrom(ip, ip.BitLen())), expr.VerdictDrop)
	}
	for _, prefix := range p.Allowed {
		add(addressMatch(prefix), expr.VerdictAccept)
	}
	if err := connection.Flush(); err != nil {
		return ErrBoundary
	}
	return nil
}

// The media process cannot clear or replace the network boundary after exec.
func DropPrivileges() error {
	if err := unix.Prctl(unix.PR_CAPBSET_DROP, unix.CAP_NET_ADMIN, 0, 0, 0); err != nil {
		return ErrBoundary
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var capabilities [2]unix.CapUserData
	if err := unix.Capget(&header, &capabilities[0]); err != nil {
		return ErrBoundary
	}
	index := unix.CAP_NET_ADMIN / 32
	mask := uint32(1) << uint(unix.CAP_NET_ADMIN%32)
	capabilities[index].Effective &^= mask
	capabilities[index].Permitted &^= mask
	capabilities[index].Inheritable &^= mask
	if err := unix.Capset(&header, &capabilities[0]); err != nil {
		return ErrBoundary
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return ErrBoundary
	}
	return nil
}
