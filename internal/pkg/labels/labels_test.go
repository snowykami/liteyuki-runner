// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package labels

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gotest.tools/v3/assert"
)

func TestParse(t *testing.T) {
	tests := []struct {
		args    string
		want    *Label
		wantErr bool
	}{
		{
			args: "ubuntu:docker://node:18",
			want: &Label{
				Name:   "ubuntu",
				Schema: "docker",
				Arg:    "//node:18",
			},
			wantErr: false,
		},
		{
			args: "ubuntu:host",
			want: &Label{
				Name:   "ubuntu",
				Schema: "host",
				Arg:    "",
			},
			wantErr: false,
		},
		{
			args: "ubuntu",
			want: &Label{
				Name:   "ubuntu",
				Schema: "host",
				Arg:    "",
			},
			wantErr: false,
		},
		{
			args:    "ubuntu:vm:ubuntu-18.04",
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.args, func(t *testing.T) {
			got, err := Parse(tt.args)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.DeepEqual(t, got, tt.want)
		})
	}
}
