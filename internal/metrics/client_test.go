package metrics

import (
	"testing"

	"github.com/prometheus/common/model"
)

func TestScalarValue(t *testing.T) {
	tests := []struct {
		name string
		val  model.Value
		want float64
	}{
		{
			name: "scalar",
			val:  &model.Scalar{Value: 12.5},
			want: 12.5,
		},
		{
			name: "single vector sample",
			val: model.Vector{
				{Value: 7},
			},
			want: 7,
		},
		{
			name: "empty vector",
			val:  model.Vector{},
			want: 0,
		},
		{
			name: "multiple vector samples",
			val: model.Vector{
				{Value: 1},
				{Value: 2},
			},
			want: 0,
		},
		{
			name: "unexpected value type",
			val:  model.Matrix{},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scalarValue(tt.val); got != tt.want {
				t.Fatalf("scalarValue() = %v, want %v", got, tt.want)
			}
		})
	}
}
