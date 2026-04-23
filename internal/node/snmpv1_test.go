package node

import (
	"net"
	"testing"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"golang.org/x/net/ipv4"

	"traptunnel/internal/config"
)

func TestExtractSNMPv1AgentAddr(t *testing.T) {
	t.Parallel()

	payload := buildSNMPMessage(t, 0, snmpV1TrapPDU, net.IPv4(10, 66, 77, 88))
	got, found, err := extractSNMPv1AgentAddr(payload)
	if err != nil {
		t.Fatalf("extractSNMPv1AgentAddr returned error: %v", err)
	}
	if !found {
		t.Fatal("expected SNMPv1 trap agent-addr to be found")
	}
	if want := net.IPv4(10, 66, 77, 88).To4(); !got.Equal(want) {
		t.Fatalf("agent-addr mismatch: want %s got %s", want, got)
	}
}

func TestExtractSNMPv1AgentAddrIgnoresNonTrap(t *testing.T) {
	t.Parallel()

	payload := buildSNMPMessage(t, 0, 0xA7, net.IPv4(10, 1, 2, 3))
	got, found, err := extractSNMPv1AgentAddr(payload)
	if err != nil {
		t.Fatalf("extractSNMPv1AgentAddr returned error: %v", err)
	}
	if found || got != nil {
		t.Fatalf("expected non-trap PDU to be ignored, found=%v ip=%v", found, got)
	}
}

func TestPrepareInjectedPacketOverridesSourceFromAgentAddr(t *testing.T) {
	t.Parallel()

	raw := buildIPv4UDPPacket(t,
		net.IPv4(10, 10, 1, 2),
		net.IPv4(10, 10, 1, 1),
		40123,
		162,
		buildSNMPMessage(t, 0, snmpV1TrapPDU, net.IPv4(10, 99, 88, 77)),
	)
	originalRaw := append([]byte(nil), raw...)
	cfg := config.NodeConfig{
		Inject: config.InjectConfig{
			Enabled:                 true,
			IP:                      "127.0.0.1",
			Port:                    1162,
			SNMPv1AgentAddrOverride: true,
		},
	}

	header, payload, rewrite, err := prepareInjectedPacket(raw, cfg)
	if err != nil {
		t.Fatalf("prepareInjectedPacket returned error: %v", err)
	}
	if !rewrite.SourceOverridden {
		t.Fatal("expected source override to be applied")
	}
	if rewrite.OriginalSrcIP != "10.10.1.2" || rewrite.EffectiveSrcIP != "10.99.88.77" {
		t.Fatalf("unexpected rewrite metadata: %+v", rewrite)
	}
	if got := header.Src.String(); got != "10.99.88.77" {
		t.Fatalf("unexpected header source ip: %s", got)
	}
	if got := header.Dst.String(); got != "127.0.0.1" {
		t.Fatalf("unexpected header destination ip: %s", got)
	}

	decoded := gopacket.NewPacket(append(headerBytes(t, header), payload...), layers.LayerTypeIPv4, gopacket.Default)
	ipLayer := decoded.Layer(layers.LayerTypeIPv4)
	if ipLayer == nil {
		t.Fatal("decoded packet missing ipv4 layer")
	}
	ip := ipLayer.(*layers.IPv4)
	if got := ip.SrcIP.String(); got != "10.99.88.77" {
		t.Fatalf("decoded packet source ip mismatch: %s", got)
	}
	if got := ip.DstIP.String(); got != "127.0.0.1" {
		t.Fatalf("decoded packet destination ip mismatch: %s", got)
	}

	udpLayer := decoded.Layer(layers.LayerTypeUDP)
	if udpLayer == nil {
		t.Fatal("decoded packet missing udp layer")
	}
	udp := udpLayer.(*layers.UDP)
	if int(udp.DstPort) != 1162 {
		t.Fatalf("decoded packet destination port mismatch: %d", udp.DstPort)
	}
	if int(udp.SrcPort) != 40123 {
		t.Fatalf("decoded packet source port mismatch: %d", udp.SrcPort)
	}
	if string(raw) != string(originalRaw) {
		t.Fatal("prepareInjectedPacket mutated the original raw frame")
	}
}

func TestPrepareInjectedPacketLeavesSourceWhenDisabled(t *testing.T) {
	t.Parallel()

	raw := buildIPv4UDPPacket(t,
		net.IPv4(10, 10, 1, 2),
		net.IPv4(10, 10, 1, 1),
		40123,
		162,
		buildSNMPMessage(t, 0, snmpV1TrapPDU, net.IPv4(10, 99, 88, 77)),
	)
	cfg := config.NodeConfig{
		Inject: config.InjectConfig{
			Enabled:                 true,
			IP:                      "127.0.0.1",
			Port:                    162,
			SNMPv1AgentAddrOverride: false,
		},
	}

	header, _, rewrite, err := prepareInjectedPacket(raw, cfg)
	if err != nil {
		t.Fatalf("prepareInjectedPacket returned error: %v", err)
	}
	if rewrite.SourceOverridden {
		t.Fatalf("did not expect source override: %+v", rewrite)
	}
	if got := header.Src.String(); got != "10.10.1.2" {
		t.Fatalf("unexpected source ip: %s", got)
	}
}

func buildSNMPMessage(t *testing.T, version int, pduTag byte, agentIP net.IP) []byte {
	t.Helper()

	varBindList := tlvEncode(berTagSequence, tlvEncode(berTagSequence,
		tlvEncode(berTagOID, []byte{0x2b, 0x06, 0x01, 0x02, 0x01, 0x01, 0x01, 0x00}),
		tlvEncode(berTagOctetString, []byte("trap-test")),
	))
	pdu := tlvEncode(pduTag,
		tlvEncode(berTagOID, []byte{0x2b, 0x06, 0x01, 0x04, 0x01, 0xbf, 0x08, 0x02, 0x03, 0x00, 0x01}),
		tlvEncode(snmpIPAddrTag, agentIP.To4()),
		tlvEncode(berTagInteger, encodeInt(6)),
		tlvEncode(berTagInteger, encodeInt(1)),
		tlvEncode(0x43, encodeInt(12345)),
		varBindList,
	)
	return tlvEncode(berTagSequence,
		tlvEncode(berTagInteger, encodeInt(version)),
		tlvEncode(berTagOctetString, []byte("public")),
		pdu,
	)
}

func buildIPv4UDPPacket(t *testing.T, srcIP, dstIP net.IP, srcPort, dstPort int, udpPayload []byte) []byte {
	t.Helper()

	ip := &layers.IPv4{
		Version:  4,
		IHL:      5,
		TTL:      64,
		Protocol: layers.IPProtocolUDP,
		SrcIP:    srcIP.To4(),
		DstIP:    dstIP.To4(),
	}
	udp := &layers.UDP{
		SrcPort: layers.UDPPort(srcPort),
		DstPort: layers.UDPPort(dstPort),
	}
	if err := udp.SetNetworkLayerForChecksum(ip); err != nil {
		t.Fatalf("SetNetworkLayerForChecksum failed: %v", err)
	}

	buffer := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buffer, gopacket.SerializeOptions{
		ComputeChecksums: true,
		FixLengths:       true,
	}, ip, udp, gopacket.Payload(udpPayload)); err != nil {
		t.Fatalf("SerializeLayers failed: %v", err)
	}
	return buffer.Bytes()
}

func headerBytes(t *testing.T, header *ipv4.Header) []byte {
	t.Helper()
	wire, err := header.Marshal()
	if err != nil {
		t.Fatalf("header.Marshal failed: %v", err)
	}
	return wire
}

func tlvEncode(tag byte, parts ...[]byte) []byte {
	length := 0
	for _, part := range parts {
		length += len(part)
	}
	buf := []byte{tag}
	buf = append(buf, encodeLength(length)...)
	for _, part := range parts {
		buf = append(buf, part...)
	}
	return buf
}

func encodeLength(length int) []byte {
	if length < 0x80 {
		return []byte{byte(length)}
	}
	if length <= 0xff {
		return []byte{0x81, byte(length)}
	}
	return []byte{0x82, byte(length >> 8), byte(length)}
}

func encodeInt(v int) []byte {
	if v == 0 {
		return []byte{0x00}
	}

	out := make([]byte, 0, 4)
	for value := v; value > 0; value >>= 8 {
		out = append([]byte{byte(value)}, out...)
	}
	if out[0]&0x80 != 0 {
		out = append([]byte{0x00}, out...)
	}
	return out
}
