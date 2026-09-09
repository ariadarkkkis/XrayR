package limiter

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xtls/xray-core/common/buf"
	"golang.org/x/time/rate"

	"github.com/ariadarkkkis/XrayR/api"
)

func TestDeleteInboundUsersClearsRuntimeState(t *testing.T) {
	const (
		tag     = "fixture"
		userKey = "fixture|user@example.com|7"
	)
	users := []api.UserInfo{{UID: 7, Email: "user@example.com", SpeedLimit: 1, DeviceLimit: 1}}
	limiter := New()
	require.NoError(t, limiter.AddInboundLimiter(tag, 0, &users, nil))

	value, ok := limiter.InboundInfo.Load(tag)
	require.True(t, ok)
	inboundInfo := value.(*InboundInfo)
	inboundInfo.BucketHub.Store(userKey, rate.NewLimiter(1, 1))
	inboundInfo.UserOnlineIP.Store(userKey, new(sync.Map))

	require.NoError(t, limiter.DeleteInboundUsers(tag, []string{userKey}))
	_, userExists := inboundInfo.UserInfo.Load(userKey)
	_, bucketExists := inboundInfo.BucketHub.Load(userKey)
	_, onlineExists := inboundInfo.UserOnlineIP.Load(userKey)
	require.False(t, userExists)
	require.False(t, bucketExists)
	require.False(t, onlineExists)
}

func TestRateReaderThrottlesBuffersLargerThanBurst(t *testing.T) {
	const bytesPerSecond = 1000
	reader := &singleMultiBufferReader{buffer: buf.MultiBuffer{buf.FromBytes(make([]byte, 1100))}}
	limited := New().RateReader(reader, rate.NewLimiter(bytesPerSecond, bytesPerSecond))

	started := time.Now()
	result, err := limited.ReadMultiBuffer()
	require.NoError(t, err)
	buf.ReleaseMulti(result)
	require.GreaterOrEqual(t, time.Since(started), 75*time.Millisecond)
}

type singleMultiBufferReader struct {
	buffer buf.MultiBuffer
}

func (r *singleMultiBufferReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	result := r.buffer
	r.buffer = nil
	return result, nil
}

func (r *singleMultiBufferReader) ReadMultiBufferTimeout(time.Duration) (buf.MultiBuffer, error) {
	return r.ReadMultiBuffer()
}
