// Code generated by "stringer -type=SinkType"; DO NOT EDIT.

package types

import "strconv"

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	// Re-run the stringer command to generate them again.
	var x [1]struct{}
	_ = x[Dummy-0]
	_ = x[StdOut-1]
	_ = x[StdErr-2]
	_ = x[InfluxUDP-3]
	_ = x[InfluxHTTP-4]
	_ = x[Elastic-5]
}

const _SinkType_name = "DummyStdOutStdErrInfluxUDPInfluxHTTPElastic"

var _SinkType_index = [...]uint8{0, 5, 11, 17, 26, 36, 43}

func (i SinkType) String() string {
	if i >= SinkType(len(_SinkType_index)-1) {
		return "SinkType(" + strconv.FormatInt(int64(i), 10) + ")"
	}
	return _SinkType_name[_SinkType_index[i]:_SinkType_index[i+1]]
}
