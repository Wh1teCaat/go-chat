package cache

import "strconv"

func UserProfileKey(userID uint) string {
	return "user:profile:" + strconv.FormatUint(uint64(userID), 10)
}

func GroupInfoKey(groupID uint) string {
	return "group:info:" + strconv.FormatUint(uint64(groupID), 10)
}
