package controller

import (
	"errors"
	"fmt"
	"reflect"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/task"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/features/stats"

	"github.com/ariadarkkkis/XrayR/api"
	"github.com/ariadarkkkis/XrayR/api/newV2board"
	"github.com/ariadarkkkis/XrayR/app/mydispatcher"
	"github.com/ariadarkkkis/XrayR/common/limiter"
	"github.com/ariadarkkkis/XrayR/common/mylego"
	"github.com/ariadarkkkis/XrayR/common/serverstatus"
)

type LimitInfo struct {
	end               int64
	currentSpeedLimit int
	originSpeedLimit  uint64
}

type Controller struct {
	server               *core.Instance
	config               *Config
	clientInfo           api.ClientInfo
	apiClient            api.API
	nodeInfo             *api.NodeInfo
	Tag                  string
	userList             *[]api.UserInfo
	tasks                []periodicTask
	limitedUsers         map[api.UserInfo]LimitInfo
	warnedUsers          map[api.UserInfo]int
	pendingOnlineUsers   []api.OnlineUser
	pendingDetectResults []api.DetectResult
	panelType            string
	ibm                  inbound.Manager
	obm                  outbound.Manager
	stm                  stats.Manager
	dispatcher           *mydispatcher.DefaultDispatcher
	startAt              time.Time
	logger               *log.Entry
}

type periodicTask struct {
	tag string
	*task.Periodic
}

// New return a Controller service with default parameters.
func New(server *core.Instance, api api.API, config *Config, panelType string) *Controller {
	logger := log.NewEntry(log.StandardLogger()).WithFields(log.Fields{
		"Host": api.Describe().APIHost,
		"Type": api.Describe().NodeType,
		"ID":   api.Describe().NodeID,
	})
	controller := &Controller{
		server:     server,
		config:     config,
		apiClient:  api,
		panelType:  panelType,
		ibm:        server.GetFeature(inbound.ManagerType()).(inbound.Manager),
		obm:        server.GetFeature(outbound.ManagerType()).(outbound.Manager),
		stm:        server.GetFeature(stats.ManagerType()).(stats.Manager),
		dispatcher: server.GetFeature(routing.DispatcherType()).(*mydispatcher.DefaultDispatcher),
		startAt:    time.Now(),
		logger:     logger,
	}

	return controller
}

// Start implement the Start() function of the service interface
func (c *Controller) Start() error {
	if err := c.StartWithoutScheduling(); err != nil {
		return err
	}
	c.schedulePeriodicTasks()
	return nil
}

// StartWithoutScheduling initializes the controller without starting timers.
// It is intended for deterministic reconciliation runs and lifecycle-managed callers.
func (c *Controller) StartWithoutScheduling() error {
	c.clientInfo = c.apiClient.Describe()
	// First fetch Node Info
	newNodeInfo, err := c.apiClient.GetNodeInfo()
	if err != nil {
		return err
	}
	if newNodeInfo.Port == 0 {
		return errors.New("server port must > 0")
	}
	c.nodeInfo = newNodeInfo
	c.Tag = c.buildNodeTag()

	// Add new tag
	err = c.addNewTag(newNodeInfo)
	if err != nil {
		return err
	}
	// Update user
	userInfo, err := c.apiClient.GetUserList()
	if err != nil {
		return err
	}

	// sync controller userList
	c.userList = userInfo

	err = c.addNewUser(userInfo, newNodeInfo)
	if err != nil {
		return err
	}

	// Add Limiter
	if err := c.AddInboundLimiter(c.Tag, newNodeInfo.SpeedLimit, userInfo, c.config.GlobalDeviceLimitConfig); err != nil {
		c.logger.Print(err)
	}

	// Update alive user list
	if v2b, ok := c.apiClient.(*newV2board.APIClient); ok {
		if v, ok := c.dispatcher.Limiter.InboundInfo.Load(c.Tag); ok {
			inboundinfo := v.(*limiter.InboundInfo)
			inboundinfo.AliveList = v2b.AliveMap.Alive
		}
	}

	// Add Rule Manager
	if !c.config.DisableGetRule {
		if ruleList, err := c.apiClient.GetNodeRule(); err != nil {
			c.logger.Printf("Get rule list filed: %s", err)
		} else if len(*ruleList) > 0 {
			if err := c.UpdateRule(c.Tag, *ruleList); err != nil {
				c.logger.Print(err)
			}
		}
	}

	// Init AutoSpeedLimitConfig
	if c.config.AutoSpeedLimitConfig == nil {
		c.config.AutoSpeedLimitConfig = &AutoSpeedLimitConfig{0, 0, 0, 0}
	}
	if c.config.AutoSpeedLimitConfig.Limit > 0 {
		c.limitedUsers = make(map[api.UserInfo]LimitInfo)
		c.warnedUsers = make(map[api.UserInfo]int)
	}

	return nil
}

func (c *Controller) schedulePeriodicTasks() {
	// Add periodic tasks
	c.tasks = append(c.tasks,
		periodicTask{
			tag: "node monitor",
			Periodic: &task.Periodic{
				Interval: time.Duration(c.config.UpdatePeriodic) * time.Second,
				Execute:  c.nodeInfoMonitor,
			}},
		periodicTask{
			tag: "user monitor",
			Periodic: &task.Periodic{
				Interval: time.Duration(c.config.UpdatePeriodic) * time.Second,
				Execute:  c.userInfoMonitor,
			}},
	)

	// Check cert service in need
	if c.nodeInfo.EnableTLS && !c.config.EnableREALITY {
		c.tasks = append(c.tasks, periodicTask{
			tag: "cert monitor",
			Periodic: &task.Periodic{
				Interval: time.Duration(c.config.UpdatePeriodic) * time.Second * 60,
				Execute:  c.certMonitor,
			}})
	}

	// Start periodic tasks
	for i := range c.tasks {
		c.logger.Printf("Start %s periodic task", c.tasks[i].tag)
		go c.tasks[i].Start()
	}
}

// Close implement the Close() function of the service interface
func (c *Controller) Close() error {
	for i := range c.tasks {
		if c.tasks[i].Periodic != nil {
			if err := c.tasks[i].Periodic.Close(); err != nil {
				c.logger.Panicf("%s periodic task close failed: %s", c.tasks[i].tag, err)
			}
		}
	}

	return nil
}

// ReconcileAndReportOnce performs exactly one panel refresh and reporting pass.
// It does not create timers, wait for signals, or start background work.
func (c *Controller) ReconcileAndReportOnce() error {
	return errors.Join(c.reconcileOnce(), c.reportOnce())
}

func (c *Controller) nodeInfoMonitor() (err error) {
	// delay to start
	if time.Since(c.startAt) < time.Duration(c.config.UpdatePeriodic)*time.Second {
		return nil
	}
	if err := c.reconcileOnce(); err != nil {
		c.logger.Print(err)
	}
	return nil
}

func (c *Controller) reconcileOnce() error {
	var cycleErrors []error

	// First fetch Node Info
	var nodeInfoChanged = true
	newNodeInfo, err := c.apiClient.GetNodeInfo()
	if err != nil {
		if err.Error() == api.NodeNotModified {
			nodeInfoChanged = false
			newNodeInfo = c.nodeInfo
		} else {
			return fmt.Errorf("refresh node info: %w", err)
		}
	}
	if newNodeInfo.Port == 0 {
		return errors.New("server port must > 0")
	}

	// Update User
	var usersChanged = true
	newUserInfo, err := c.apiClient.GetUserList()

	// Update alive user list
	if v2b, ok := c.apiClient.(*newV2board.APIClient); ok {
		if v, ok := c.dispatcher.Limiter.InboundInfo.Load(c.Tag); ok {
			inboundinfo := v.(*limiter.InboundInfo)
			inboundinfo.AliveList = v2b.AliveMap.Alive
		}
	}
	if err != nil {
		if err.Error() == api.UserNotModified {
			usersChanged = false
			newUserInfo = c.userList
		} else {
			return fmt.Errorf("refresh user list: %w", err)
		}
	}

	runtimeReplaced := false
	// If nodeInfo changed
	if nodeInfoChanged {
		if !reflect.DeepEqual(c.nodeInfo, newNodeInfo) {
			if err := c.replaceNodeRuntime(newNodeInfo, newUserInfo); err != nil {
				return err
			}
			runtimeReplaced = true
		} else {
			nodeInfoChanged = false
		}
	}

	// Check Rule
	if !c.config.DisableGetRule {
		if ruleList, err := c.apiClient.GetNodeRule(); err != nil {
			if err.Error() != api.RuleNotModified {
				cycleErrors = append(cycleErrors, fmt.Errorf("refresh detection rules: %w", err))
			}
		} else if len(*ruleList) > 0 {
			if err := c.UpdateRule(c.Tag, *ruleList); err != nil {
				cycleErrors = append(cycleErrors, fmt.Errorf("update detection rules: %w", err))
			}
		}
	}

	if !runtimeReplaced {
		var deleted, added []api.UserInfo
		if usersChanged {
			deleted, added = compareUserList(c.userList, newUserInfo)
			if len(added) > 0 {
				if _, err := c.buildRuntimeUsers(&added, c.nodeInfo); err != nil {
					return fmt.Errorf("validate refreshed users: %w", err)
				}
			}

			var deletedEmail []string
			if len(deleted) > 0 {
				deletedEmail = make([]string, len(deleted))
				for i, u := range deleted {
					deletedEmail[i] = fmt.Sprintf("%s|%s|%d", c.Tag, u.Email, u.UID)
				}
				if err := c.removeUsers(deletedEmail, c.Tag); err != nil {
					return fmt.Errorf("remove stale users: %w", err)
				}
				if err := c.DeleteInboundUsers(c.Tag, deletedEmail); err != nil {
					return fmt.Errorf("remove stale users from limiter: %w", err)
				}
			}
			if len(added) > 0 {
				if err := c.addNewUser(&added, c.nodeInfo); err != nil {
					return fmt.Errorf("add refreshed users: %w", err)
				}
				// Update Limiter
				if err := c.UpdateInboundLimiter(c.Tag, &added); err != nil {
					return fmt.Errorf("update inbound limiter: %w", err)
				}
			}
		}
		c.logger.Printf("%d user deleted, %d user added", len(deleted), len(added))
	}
	c.userList = newUserInfo
	return errors.Join(cycleErrors...)
}

func (c *Controller) removeOldTag(oldTag string) (err error) {
	err = c.removeInbound(oldTag)
	if err != nil {
		return err
	}
	err = c.removeOutbound(oldTag)
	if err != nil {
		return err
	}
	return nil
}

func (c *Controller) replaceNodeRuntime(newNodeInfo *api.NodeInfo, newUserInfo *[]api.UserInfo) error {
	oldNodeInfo := c.nodeInfo
	oldUserInfo := c.userList
	oldTag := c.Tag
	newTag := c.buildNodeTagFor(newNodeInfo)

	if err := c.validateNodeRuntime(newNodeInfo, newUserInfo, newTag); err != nil {
		return fmt.Errorf("validate refreshed runtime: %w", err)
	}
	if err := c.removeNodeRuntime(oldNodeInfo, oldTag); err != nil {
		restoreErr := c.restoreNodeRuntime(oldNodeInfo, oldUserInfo, oldTag, newNodeInfo, newTag)
		return errors.Join(fmt.Errorf("remove old runtime tag: %w", err), restoreErr)
	}

	c.nodeInfo = newNodeInfo
	c.Tag = newTag
	if err := c.addNewTag(newNodeInfo); err != nil {
		restoreErr := c.restoreNodeRuntime(oldNodeInfo, oldUserInfo, oldTag, newNodeInfo, newTag)
		return errors.Join(fmt.Errorf("add refreshed runtime tag: %w", err), restoreErr)
	}
	if err := c.addNewUser(newUserInfo, newNodeInfo); err != nil {
		restoreErr := c.restoreNodeRuntime(oldNodeInfo, oldUserInfo, oldTag, newNodeInfo, newTag)
		return errors.Join(fmt.Errorf("add refreshed users: %w", err), restoreErr)
	}
	if err := c.AddInboundLimiter(newTag, newNodeInfo.SpeedLimit, newUserInfo, c.config.GlobalDeviceLimitConfig); err != nil {
		restoreErr := c.restoreNodeRuntime(oldNodeInfo, oldUserInfo, oldTag, newNodeInfo, newTag)
		return errors.Join(fmt.Errorf("add refreshed inbound limiter: %w", err), restoreErr)
	}
	if oldTag != newTag {
		if err := c.DeleteInboundLimiter(oldTag); err != nil {
			return fmt.Errorf("remove old inbound limiter: %w", err)
		}
	}
	return nil
}

func (c *Controller) validateNodeRuntime(nodeInfo *api.NodeInfo, userInfo *[]api.UserInfo, tag string) error {
	if _, err := c.buildRuntimeUsers(userInfo, nodeInfo); err != nil {
		return err
	}
	if nodeInfo.NodeType != "Shadowsocks-Plugin" {
		if _, err := InboundBuilder(c.config, nodeInfo, tag); err != nil {
			return err
		}
		_, err := OutboundBuilder(c.config, nodeInfo, tag)
		return err
	}

	shadowsocksNode := *nodeInfo
	shadowsocksNode.TransportProtocol = "tcp"
	shadowsocksNode.EnableTLS = false
	if _, err := InboundBuilder(c.config, &shadowsocksNode, tag); err != nil {
		return err
	}
	if _, err := OutboundBuilder(c.config, &shadowsocksNode, tag); err != nil {
		return err
	}
	pluginNode := *nodeInfo
	pluginNode.Port++
	pluginNode.NodeType = "dokodemo-door"
	pluginTag := fmt.Sprintf("dokodemo-door_%s+1", tag)
	if _, err := InboundBuilder(c.config, &pluginNode, pluginTag); err != nil {
		return err
	}
	_, err := OutboundBuilder(c.config, &pluginNode, pluginTag)
	return err
}

func (c *Controller) removeNodeRuntime(nodeInfo *api.NodeInfo, tag string) error {
	var runtimeErrors []error
	if err := c.removeOldTag(tag); err != nil {
		runtimeErrors = append(runtimeErrors, err)
	}
	if nodeInfo != nil && nodeInfo.NodeType == "Shadowsocks-Plugin" {
		if err := c.removeOldTag(fmt.Sprintf("dokodemo-door_%s+1", tag)); err != nil {
			runtimeErrors = append(runtimeErrors, err)
		}
	}
	return errors.Join(runtimeErrors...)
}

func (c *Controller) restoreNodeRuntime(oldNodeInfo *api.NodeInfo, oldUserInfo *[]api.UserInfo, oldTag string, newNodeInfo *api.NodeInfo, newTag string) error {
	_ = c.removeNodeRuntime(newNodeInfo, newTag)
	c.nodeInfo = oldNodeInfo
	c.Tag = oldTag
	var restoreErrors []error
	if err := c.addNewTag(oldNodeInfo); err != nil {
		restoreErrors = append(restoreErrors, fmt.Errorf("restore previous handlers: %w", err))
		return errors.Join(restoreErrors...)
	}
	if err := c.addNewUser(oldUserInfo, oldNodeInfo); err != nil {
		restoreErrors = append(restoreErrors, fmt.Errorf("restore previous users: %w", err))
	}
	if err := c.AddInboundLimiter(oldTag, oldNodeInfo.SpeedLimit, oldUserInfo, c.config.GlobalDeviceLimitConfig); err != nil {
		restoreErrors = append(restoreErrors, fmt.Errorf("restore previous limiter: %w", err))
	}
	return errors.Join(restoreErrors...)
}

func (c *Controller) addNewTag(newNodeInfo *api.NodeInfo) (err error) {
	if newNodeInfo.NodeType != "Shadowsocks-Plugin" {
		inboundConfig, err := InboundBuilder(c.config, newNodeInfo, c.Tag)
		if err != nil {
			return err
		}
		err = c.addInbound(inboundConfig)
		if err != nil {

			return err
		}
		outBoundConfig, err := OutboundBuilder(c.config, newNodeInfo, c.Tag)
		if err != nil {

			return err
		}
		err = c.addOutbound(outBoundConfig)
		if err != nil {

			return err
		}

	} else {
		return c.addInboundForSSPlugin(*newNodeInfo)
	}
	return nil
}

func (c *Controller) addInboundForSSPlugin(newNodeInfo api.NodeInfo) (err error) {
	// Shadowsocks-Plugin require a separate inbound for other TransportProtocol likes: ws, grpc
	fakeNodeInfo := newNodeInfo
	fakeNodeInfo.TransportProtocol = "tcp"
	fakeNodeInfo.EnableTLS = false
	// Add a regular Shadowsocks inbound and outbound
	inboundConfig, err := InboundBuilder(c.config, &fakeNodeInfo, c.Tag)
	if err != nil {
		return err
	}
	err = c.addInbound(inboundConfig)
	if err != nil {

		return err
	}
	outBoundConfig, err := OutboundBuilder(c.config, &fakeNodeInfo, c.Tag)
	if err != nil {

		return err
	}
	err = c.addOutbound(outBoundConfig)
	if err != nil {

		return err
	}
	// Add an inbound for upper streaming protocol
	fakeNodeInfo = newNodeInfo
	fakeNodeInfo.Port++
	fakeNodeInfo.NodeType = "dokodemo-door"
	dokodemoTag := fmt.Sprintf("dokodemo-door_%s+1", c.Tag)
	inboundConfig, err = InboundBuilder(c.config, &fakeNodeInfo, dokodemoTag)
	if err != nil {
		return err
	}
	err = c.addInbound(inboundConfig)
	if err != nil {

		return err
	}
	outBoundConfig, err = OutboundBuilder(c.config, &fakeNodeInfo, dokodemoTag)
	if err != nil {

		return err
	}
	err = c.addOutbound(outBoundConfig)
	if err != nil {

		return err
	}
	return nil
}

func (c *Controller) addNewUser(userInfo *[]api.UserInfo, nodeInfo *api.NodeInfo) (err error) {
	users, err := c.buildRuntimeUsers(userInfo, nodeInfo)
	if err != nil {
		return err
	}
	if err = c.addUsers(users, c.Tag); err != nil {
		return err
	}
	c.logger.Printf("Added %d new users", len(*userInfo))
	return nil
}

func (c *Controller) buildRuntimeUsers(userInfo *[]api.UserInfo, nodeInfo *api.NodeInfo) ([]*protocol.User, error) {
	users := make([]*protocol.User, 0)
	switch nodeInfo.NodeType {
	case "V2ray", "Vmess", "Vless":
		if nodeInfo.EnableVless || (nodeInfo.NodeType == "Vless" && nodeInfo.NodeType != "Vmess") {
			users = c.buildVlessUser(userInfo, nodeInfo.VlessFlow)
		} else {
			users = c.buildVmessUser(userInfo)
		}
	case "Trojan":
		users = c.buildTrojanUser(userInfo)
	case "Shadowsocks":
		users = c.buildSSUser(userInfo, nodeInfo.CypherMethod)
	case "Shadowsocks-Plugin":
		users = c.buildSSPluginUser(userInfo)
	default:
		return nil, fmt.Errorf("unsupported node type: %s", nodeInfo.NodeType)
	}

	for index, user := range users {
		if user == nil {
			return nil, fmt.Errorf("node %d user %d cannot be represented by the configured protocol", nodeInfo.NodeID, (*userInfo)[index].UID)
		}
		if _, err := user.ToMemoryUser(); err != nil {
			return nil, fmt.Errorf("node %d user %d has invalid protocol credentials: %w", nodeInfo.NodeID, (*userInfo)[index].UID, err)
		}
	}
	return users, nil
}

func compareUserList(old, new *[]api.UserInfo) (deleted, added []api.UserInfo) {
	mSrc := make(map[api.UserInfo]byte) // 按源数组建索引
	mAll := make(map[api.UserInfo]byte) // 源+目所有元素建索引

	var set []api.UserInfo // 交集

	// 1.源数组建立map
	for _, v := range *old {
		mSrc[v] = 0
		mAll[v] = 0
	}
	// 2.目数组中，存不进去，即重复元素，所有存不进去的集合就是并集
	for _, v := range *new {
		l := len(mAll)
		mAll[v] = 1
		if l != len(mAll) { // 长度变化，即可以存
			l = len(mAll)
		} else { // 存不了，进并集
			set = append(set, v)
		}
	}
	// 3.遍历交集，在并集中找，找到就从并集中删，删完后就是补集（即并-交=所有变化的元素）
	for _, v := range set {
		delete(mAll, v)
	}
	// 4.此时，mall是补集，所有元素去源中找，找到就是删除的，找不到的必定能在目数组中找到，即新加的
	for v := range mAll {
		_, exist := mSrc[v]
		if exist {
			deleted = append(deleted, v)
		} else {
			added = append(added, v)
		}
	}

	return deleted, added
}

func limitUser(c *Controller, user api.UserInfo, silentUsers *[]api.UserInfo) {
	c.limitedUsers[user] = LimitInfo{
		end:               time.Now().Unix() + int64(c.config.AutoSpeedLimitConfig.LimitDuration*60),
		currentSpeedLimit: c.config.AutoSpeedLimitConfig.LimitSpeed,
		originSpeedLimit:  user.SpeedLimit,
	}
	c.logger.Printf("Limit User: %s Speed: %d End: %s", c.buildUserTag(&user), c.config.AutoSpeedLimitConfig.LimitSpeed, time.Unix(c.limitedUsers[user].end, 0).Format("01-02 15:04:05"))
	user.SpeedLimit = uint64((c.config.AutoSpeedLimitConfig.LimitSpeed * 1000000) / 8)
	*silentUsers = append(*silentUsers, user)
}

func (c *Controller) userInfoMonitor() (err error) {
	// delay to start
	if time.Since(c.startAt) < time.Duration(c.config.UpdatePeriodic)*time.Second {
		return nil
	}
	if err := c.reportOnce(); err != nil {
		c.logger.Print(err)
	}
	return nil
}

func (c *Controller) reportOnce() error {
	var cycleErrors []error

	// Get server status
	CPU, Mem, Disk, Uptime, err := serverstatus.GetSystemInfo()
	if err != nil {
		c.logger.Print(err)
	}
	err = c.apiClient.ReportNodeStatus(
		&api.NodeStatus{
			CPU:    CPU,
			Mem:    Mem,
			Disk:   Disk,
			Uptime: Uptime,
		})
	if err != nil {
		cycleErrors = append(cycleErrors, fmt.Errorf("report node status: %w", err))
	}
	// Unlock users
	if c.config.AutoSpeedLimitConfig.Limit > 0 && len(c.limitedUsers) > 0 {
		c.logger.Printf("Limited users:")
		toReleaseUsers := make([]api.UserInfo, 0)
		for user, limitInfo := range c.limitedUsers {
			if time.Now().Unix() > limitInfo.end {
				user.SpeedLimit = limitInfo.originSpeedLimit
				toReleaseUsers = append(toReleaseUsers, user)
				c.logger.Printf("User: %s Speed: %d End: nil (Unlimit)", c.buildUserTag(&user), user.SpeedLimit)
				delete(c.limitedUsers, user)
			} else {
				c.logger.Printf("User: %s Speed: %d End: %s", c.buildUserTag(&user), limitInfo.currentSpeedLimit, time.Unix(c.limitedUsers[user].end, 0).Format("01-02 15:04:05"))
			}
		}
		if len(toReleaseUsers) > 0 {
			if err := c.UpdateInboundLimiter(c.Tag, &toReleaseUsers); err != nil {
				cycleErrors = append(cycleErrors, fmt.Errorf("release limited users: %w", err))
			}
		}
	}

	// Get User traffic
	var userTraffic []api.UserTraffic
	var upCounterList []stats.Counter
	var downCounterList []stats.Counter
	AutoSpeedLimit := int64(c.config.AutoSpeedLimitConfig.Limit)
	UpdatePeriodic := int64(c.config.UpdatePeriodic)
	limitedUsers := make([]api.UserInfo, 0)
	for _, user := range *c.userList {
		up, down, upCounter, downCounter := c.getTraffic(c.buildUserTag(&user))
		if up > 0 || down > 0 {
			// Over speed users
			if AutoSpeedLimit > 0 {
				if down > AutoSpeedLimit*1000000*UpdatePeriodic/8 || up > AutoSpeedLimit*1000000*UpdatePeriodic/8 {
					if _, ok := c.limitedUsers[user]; !ok {
						if c.config.AutoSpeedLimitConfig.WarnTimes == 0 {
							limitUser(c, user, &limitedUsers)
						} else {
							c.warnedUsers[user] += 1
							if c.warnedUsers[user] > c.config.AutoSpeedLimitConfig.WarnTimes {
								limitUser(c, user, &limitedUsers)
								delete(c.warnedUsers, user)
							}
						}
					}
				} else {
					delete(c.warnedUsers, user)
				}
			}
			userTraffic = append(userTraffic, api.UserTraffic{
				UID:      user.UID,
				Email:    user.Email,
				Upload:   up,
				Download: down})

			if upCounter != nil {
				upCounterList = append(upCounterList, upCounter)
			}
			if downCounter != nil {
				downCounterList = append(downCounterList, downCounter)
			}
		} else {
			delete(c.warnedUsers, user)
		}
	}
	if len(limitedUsers) > 0 {
		if err := c.UpdateInboundLimiter(c.Tag, &limitedUsers); err != nil {
			cycleErrors = append(cycleErrors, fmt.Errorf("apply user speed limits: %w", err))
		}
	}

	if len(userTraffic) > 0 {
		var err error // Define an empty error
		if !c.config.DisableUploadTraffic {
			err = c.apiClient.ReportUserTraffic(&userTraffic)
		}
		// If report traffic error, not clear the traffic
		if err != nil {
			cycleErrors = append(cycleErrors, fmt.Errorf("report user traffic: %w", err))
		} else {
			c.resetTraffic(&upCounterList, &downCounterList)
		}
	}

	// Report Online info. Collection drains the limiter, so keep a pending copy
	// until the panel acknowledges it.
	onlineDevice, err := c.GetOnlineDevice(c.Tag)
	if err != nil {
		cycleErrors = append(cycleErrors, fmt.Errorf("collect online users: %w", err))
	} else if len(*onlineDevice) > 0 {
		// Only report user has traffic > 100kb to allow ping test
		var result []api.OnlineUser
		var nocountUID = make(map[int]struct{})
		for _, traffic := range userTraffic {
			total := traffic.Upload + traffic.Download
			if total < int64(c.config.DeviceOnlineMinTraffic*1000) {
				nocountUID[traffic.UID] = struct{}{}
			}
		}
		for _, online := range *onlineDevice {
			if _, ok := nocountUID[online.UID]; !ok {
				result = append(result, online)
			}
		}
		c.pendingOnlineUsers = appendUniqueOnlineUsers(c.pendingOnlineUsers, result)
	}

	if len(c.pendingOnlineUsers) > 0 {
		if err := c.apiClient.ReportNodeOnlineUsers(&c.pendingOnlineUsers); err != nil {
			cycleErrors = append(cycleErrors, fmt.Errorf("report online users: %w", err))
		} else {
			log.Printf("Reported %d online users", len(c.pendingOnlineUsers))
			c.pendingOnlineUsers = nil
		}
	}

	// Report Illegal user. Detection collection is destructive, so retain the
	// batch until the panel acknowledges it.
	detectResult, err := c.GetDetectResult(c.Tag)
	if err != nil {
		cycleErrors = append(cycleErrors, fmt.Errorf("collect detection results: %w", err))
	} else if len(*detectResult) > 0 {
		c.pendingDetectResults = appendUniqueDetectResults(c.pendingDetectResults, *detectResult)
	}
	if len(c.pendingDetectResults) > 0 {
		if err := c.apiClient.ReportIllegal(&c.pendingDetectResults); err != nil {
			cycleErrors = append(cycleErrors, fmt.Errorf("report detection results: %w", err))
		} else {
			c.logger.Printf("Report %d illegal behaviors", len(c.pendingDetectResults))
			c.pendingDetectResults = nil
		}
	}
	return errors.Join(cycleErrors...)
}

func appendUniqueOnlineUsers(existing, additions []api.OnlineUser) []api.OnlineUser {
	seen := make(map[api.OnlineUser]struct{}, len(existing)+len(additions))
	for _, online := range existing {
		seen[online] = struct{}{}
	}
	for _, online := range additions {
		if _, ok := seen[online]; ok {
			continue
		}
		seen[online] = struct{}{}
		existing = append(existing, online)
	}
	return existing
}

func appendUniqueDetectResults(existing, additions []api.DetectResult) []api.DetectResult {
	seen := make(map[api.DetectResult]struct{}, len(existing)+len(additions))
	for _, result := range existing {
		seen[result] = struct{}{}
	}
	for _, result := range additions {
		if _, ok := seen[result]; ok {
			continue
		}
		seen[result] = struct{}{}
		existing = append(existing, result)
	}
	return existing
}

func (c *Controller) buildNodeTag() string {
	return c.buildNodeTagFor(c.nodeInfo)
}

func (c *Controller) buildNodeTagFor(nodeInfo *api.NodeInfo) string {
	return fmt.Sprintf("%s_%s_%d", nodeInfo.NodeType, c.config.ListenIP, nodeInfo.Port)
}

// func (c *Controller) logPrefix() string {
// 	return fmt.Sprintf("[%s] %s(ID=%d)", c.clientInfo.APIHost, c.nodeInfo.NodeType, c.nodeInfo.NodeID)
// }

// Check Cert
func (c *Controller) certMonitor() error {
	if c.nodeInfo.EnableTLS && !c.config.EnableREALITY {
		switch c.config.CertConfig.CertMode {
		case "dns", "http", "tls":
			lego, err := mylego.New(c.config.CertConfig)
			if err != nil {
				c.logger.Print(err)
			}
			// Xray-core supports the OcspStapling certification hot renew
			_, _, _, err = lego.RenewCert()
			if err != nil {
				c.logger.Print(err)
			}
		}
	}
	return nil
}
