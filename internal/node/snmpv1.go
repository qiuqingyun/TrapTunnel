package node

import (
	"encoding/binary"
	"fmt"
	"net"
)

const (
	berTagSequence    = 0x30
	berTagInteger     = 0x02
	berTagOctetString = 0x04
	berTagOID         = 0x06
	snmpV1TrapPDU     = 0xA4
	snmpIPAddrTag     = 0x40
)

func extractSNMPv1AgentAddr(payload []byte) (net.IP, bool, error) {
	message, rest, err := parseTLV(payload)
	if err != nil {
		return nil, false, err
	}
	if len(rest) != 0 || message.tag != berTagSequence {
		return nil, false, nil
	}

	version, remaining, err := parseTLV(message.value)
	if err != nil {
		return nil, false, err
	}
	if version.tag != berTagInteger {
		return nil, false, nil
	}
	versionValue, err := parsePositiveInteger(version.value)
	if err != nil {
		return nil, false, err
	}
	if versionValue != 0 {
		return nil, false, nil
	}

	community, remaining, err := parseTLV(remaining)
	if err != nil {
		return nil, false, err
	}
	if community.tag != berTagOctetString {
		return nil, false, nil
	}

	pdu, _, err := parseTLV(remaining)
	if err != nil {
		return nil, false, err
	}
	if pdu.tag != snmpV1TrapPDU {
		return nil, false, nil
	}

	enterprise, remaining, err := parseTLV(pdu.value)
	if err != nil {
		return nil, false, err
	}
	if enterprise.tag != berTagOID {
		return nil, false, nil
	}

	agentAddr, _, err := parseTLV(remaining)
	if err != nil {
		return nil, false, err
	}
	if agentAddr.tag != snmpIPAddrTag || len(agentAddr.value) != net.IPv4len {
		return nil, false, nil
	}

	ip := net.IPv4(agentAddr.value[0], agentAddr.value[1], agentAddr.value[2], agentAddr.value[3]).To4()
	if ip == nil {
		return nil, false, nil
	}
	return ip, true, nil
}

type tlv struct {
	tag   byte
	value []byte
}

func parseTLV(data []byte) (tlv, []byte, error) {
	if len(data) < 2 {
		return tlv{}, nil, fmt.Errorf("short BER value")
	}

	tag := data[0]
	length, n, err := parseLength(data[1:])
	if err != nil {
		return tlv{}, nil, err
	}
	if len(data) < 1+n+length {
		return tlv{}, nil, fmt.Errorf("truncated BER value")
	}

	start := 1 + n
	end := start + length
	return tlv{
		tag:   tag,
		value: data[start:end],
	}, data[end:], nil
}

func parseLength(data []byte) (int, int, error) {
	if len(data) == 0 {
		return 0, 0, fmt.Errorf("missing BER length")
	}

	if data[0]&0x80 == 0 {
		return int(data[0]), 1, nil
	}

	size := int(data[0] & 0x7f)
	if size == 0 {
		return 0, 0, fmt.Errorf("indefinite BER length is not supported")
	}
	if size > 4 || len(data) < 1+size {
		return 0, 0, fmt.Errorf("invalid BER long length")
	}

	var length uint32
	for _, b := range data[1 : 1+size] {
		length = (length << 8) | uint32(b)
	}
	return int(length), 1 + size, nil
}

func parsePositiveInteger(data []byte) (int, error) {
	if len(data) == 0 || len(data) > 4 {
		return 0, fmt.Errorf("unsupported INTEGER length %d", len(data))
	}
	if data[0]&0x80 != 0 {
		return 0, fmt.Errorf("negative INTEGER is not supported")
	}

	buf := make([]byte, 4)
	copy(buf[4-len(data):], data)
	return int(binary.BigEndian.Uint32(buf)), nil
}
