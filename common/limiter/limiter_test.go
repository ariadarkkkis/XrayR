package limiter

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
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
