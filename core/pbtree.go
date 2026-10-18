package core

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
)

// pbm is a protobuf message without a schema, written as fields in order.
// Values are int (varint), string, []int and []string (repeated), or pbm.
// It mirrors the field maps the Google Photos app sends, which carry large
// field masks that have no generated types here.
type pbm []pbf

type pbf struct {
	num protowire.Number
	val any
}

func (m pbm) marshal() []byte {
	var b []byte
	for _, f := range m {
		b = appendPBField(b, f.num, f.val)
	}
	return b
}

func appendPBField(b []byte, num protowire.Number, val any) []byte {
	switch v := val.(type) {
	case int:
		b = protowire.AppendTag(b, num, protowire.VarintType)
		return protowire.AppendVarint(b, uint64(v))
	case string:
		b = protowire.AppendTag(b, num, protowire.BytesType)
		return protowire.AppendString(b, v)
	case []byte:
		b = protowire.AppendTag(b, num, protowire.BytesType)
		return protowire.AppendBytes(b, v)
	case pbm:
		b = protowire.AppendTag(b, num, protowire.BytesType)
		return protowire.AppendBytes(b, v.marshal())
	case []int:
		for _, item := range v {
			b = appendPBField(b, num, item)
		}
		return b
	case []string:
		for _, item := range v {
			b = appendPBField(b, num, item)
		}
		return b
	default:
		panic(fmt.Sprintf("pbm: unsupported value %T", val))
	}
}

// pbNode is one decoded protobuf value. Length-delimited values stay raw
// until read, because without a schema they may be a string, bytes or a
// nested message.
type pbNode struct {
	varint uint64
	raw    []byte
	isRaw  bool
}

// pbDecoded is a decoded message: every value of each field number, in order.
type pbDecoded map[protowire.Number][]pbNode

func decodePB(b []byte) (pbDecoded, error) {
	msg := pbDecoded{}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		b = b[n:]
		var node pbNode
		switch typ {
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			node.varint, n = v, m
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			node.raw, node.isRaw, n = v, true, m
		case protowire.Fixed32Type:
			v, m := protowire.ConsumeFixed32(b)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			node.varint, n = uint64(v), m
		case protowire.Fixed64Type:
			v, m := protowire.ConsumeFixed64(b)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			node.varint, n = v, m
		default:
			m := protowire.ConsumeFieldValue(num, typ, b)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			n = m
		}
		b = b[n:]
		msg[num] = append(msg[num], node)
	}
	return msg, nil
}

// msg returns the first value of a field path as a message, or nil.
func (m pbDecoded) msg(path ...protowire.Number) pbDecoded {
	current := m
	for _, num := range path {
		nodes := current[num]
		if len(nodes) == 0 || !nodes[0].isRaw {
			return nil
		}
		next, err := decodePB(nodes[0].raw)
		if err != nil {
			return nil
		}
		current = next
	}
	return current
}

// all returns every value of a repeated message field.
func (m pbDecoded) all(num protowire.Number) []pbDecoded {
	var out []pbDecoded
	for _, node := range m[num] {
		if !node.isRaw {
			continue
		}
		if decoded, err := decodePB(node.raw); err == nil {
			out = append(out, decoded)
		}
	}
	return out
}

// str returns the last field of a path as a string, or "".
func (m pbDecoded) str(path ...protowire.Number) string {
	b := m.bytes(path...)
	return string(b)
}

func (m pbDecoded) bytes(path ...protowire.Number) []byte {
	parent := m.msg(path[:len(path)-1]...)
	if parent == nil {
		return nil
	}
	nodes := parent[path[len(path)-1]]
	if len(nodes) == 0 || !nodes[0].isRaw {
		return nil
	}
	return nodes[0].raw
}

// uint returns the last field of a path as a number, or 0.
func (m pbDecoded) uint(path ...protowire.Number) uint64 {
	parent := m.msg(path[:len(path)-1]...)
	if parent == nil {
		return 0
	}
	nodes := parent[path[len(path)-1]]
	if len(nodes) == 0 || nodes[0].isRaw {
		return 0
	}
	return nodes[0].varint
}
