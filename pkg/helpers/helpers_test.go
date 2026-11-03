package helpers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsNonLinux(t *testing.T) {
	for _, tc := range []struct {
		labels map[string]string
		want   bool
	}{
		{nil, false},
		{map[string]string{OSLabel: "linux"}, false},
		{map[string]string{OSLabel: "windows"}, true},
		{map[string]string{OSLabel: "aix"}, true},
		{map[string]string{OSLabel: "freebsd"}, true},
	} {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: tc.labels}}
		assert.Equal(t, tc.want, IsNonLinux(node), "labels %v", tc.labels)
	}
}
