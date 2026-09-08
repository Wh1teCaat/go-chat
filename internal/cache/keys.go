package cache

import "strconv"

// UserProfileKey 返回指定用户资料的缓存键。
func UserProfileKey(userID uint) string {
	return "v2:user:profile:" + strconv.FormatUint(uint64(userID), 10)
}

// GroupInfoKey 返回指定群组信息的缓存键。
func GroupInfoKey(groupID uint) string {
	return "v2:group:info:" + strconv.FormatUint(uint64(groupID), 10)
}
