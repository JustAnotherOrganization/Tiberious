package db

import (
	"bytes"
	"strconv"
	"tiberious/logger"
	"tiberious/types"

	"gopkg.in/redis.v3"

	"github.com/pborman/uuid"
	"github.com/pkg/errors"
)

/*
Data map:
	Users:
		Main Key: "user-"+<user-type>+"-"+<loginname>+<uuid> (hash)
		Joined Rooms: "user-"+<user-type+"-"+<uuid>+"rooms" (set)
		Joined Groups: "user-"+<user-type+"-"+<uuid>+"groups" (set)
	Rooms:
		Info: "room-"+<group name>+<room name>+"-info" (hash)
		User List: "room-"+<group name>+<room name>+"-list" (set)
	Groups:
		Info: "group-"+<group name>+"info" (hash)
		User List: "group-"+<group name>+"-users" (set)
		Room List: "group-"+<group name>+"-rooms" (set)
*/

var (
	errMissingDatabaseHost = errors.New("Missing DatabaseHost in config file")
)

type (
	rdisClient interface {
		updateSet(key string, new []string)
		getKeySet(search string) ([]string, error)
		writeUserData(user *types.User) error
		writeRoomData(room *types.Room) error
		writeGroupData(group *types.Group) error
		getUserData(id string) (*types.User, error)
		getRoomData(gname, rname string) (*types.Room, error)
		getGroupData(gname string) (*types.Group, error)
		deleteUser(user *types.User) error
	}

	rClient struct {
		*redis.Client
	}
)

func (db *dbClient) newRedisClient() (rdisClient, error) {
	if db.config.DatabaseAddress == "" {
		return nil, errMissingDatabaseHost
	}

	if db.config.DatabasePass == "" {
		logger.Info("Insecure redis database is not recommended")
	}

	r := &rClient{}
	r.Client = redis.NewClient(&redis.Options{
		//r.Client = redis.NewClient(&redis.Options{
		Addr:     db.config.DatabaseAddress,
		Password: db.config.DatabasePass,
		DB:       db.config.DatabaseUser,
	})

	// Confirm we can communicate with the redis instance.
	_, err := r.Ping().Result()
	if err != nil {
		return nil, errors.Wrap(err, "r.Pint().Result")
	}

	return r, nil
}

// Stupid helper function because redis only handles strings.
func strbool(b bool) string {
	if b {
		return "true"
	}

	return "false"
}

func boolstr(s string) bool {
	if s == "true" {
		return true
	}

	return false
}

/* This seems extremely cumbersome but it's the best way I can think to handle
 * this without deleting the entire set and recreating it. */
func (r *rClient) updateSet(key string, new []string) {
	old, err := r.Client.SMembers(key).Result()
	if err != nil {
		logger.Error(err)
	}

	for _, o := range old {
		var rem = true
		for _, n := range new {
			if o == n {
				rem = false
			}
		}

		if rem {
			if err := r.Client.SRem(key, o).Err(); err != nil {
				logger.Error(err)
			}
		}
	}

	for _, n := range new {
		var add = true
		for _, o := range old {
			if n == o {
				add = false
			}
		}

		if add {
			if err := r.Client.SAdd(key, n).Err(); err != nil {
				logger.Error(err)
			}
		}
	}
}

func (r *rClient) getKeySet(search string) ([]string, error) {
	return r.Client.Keys(search).Result()
}

func (r *rClient) writeUserData(user *types.User) error {
	if err := r.Client.HMSet(
		"user-"+user.Type+"-"+user.LoginName+"-"+user.ID.String(),
		"id", user.ID.String(),
		"type", user.Type,
		"username", user.Username,
		"loginname", user.LoginName,
		"email", user.Email,
		"password", user.Password,
		"salt", user.Salt,
		"connected", strbool(user.Connected),
	).Err(); err != nil {
		return errors.Wrap(err, "r.Client.HMSet")
	}

	go r.updateSet("user-"+user.Type+"-"+user.ID.String()+"-rooms", user.Rooms)
	go r.updateSet("user-"+user.Type+"-"+user.ID.String()+"-groups", user.Groups)

	return nil
}

func (r *rClient) writeRoomData(room *types.Room) error {
	if err := r.Client.HMSet("room-"+room.Group+"-"+room.Title+"-info", "title", room.Title, "group", room.Group, "private", strbool(room.Private)).Err(); err != nil {
		return errors.Wrap(err, "r.Client.HMSet")
	}

	var slice []string
	for _, u := range room.Users {
		slice = append(slice, u.ID.String())
	}

	go r.updateSet("room-"+room.Group+"-"+room.Title+"-list", slice)

	return nil
}

func (r *rClient) writeGroupData(group *types.Group) error {
	if err := r.Client.HSet("group-"+group.Title+"-info", "title", group.Title).Err(); err != nil {
		return errors.Wrap(err, "r.Client.HMSet")
	}

	var slice []string
	for _, r := range group.Rooms {
		slice = append(slice, r.Title)
	}
	go r.updateSet("group-"+group.Title+"-rooms", slice)

	slice = nil
	for _, u := range group.Users {
		slice = append(slice, u.ID.String())
	}

	go r.updateSet("group-"+group.Title+"-users", slice)

	return nil
}

func (r *rClient) getUserData(id string) (*types.User, error) {
	keys, err := r.getKeySet("user-*-*-" + id)
	if err != nil {
		return nil, errors.Wrap(err, "r.getKeySet")
	}

	if len(keys) == 0 {
		return nil, nil
	}

	info, err := r.Client.HGetAllMap(keys[0]).Result()
	if err != nil {
		return nil, errors.Wrap(err, "r.Client.HGetAllMap")
	}

	user := &types.User{
		ID:        uuid.Parse(info["id"]),
		Type:      info["type"],
		Username:  info["username"],
		LoginName: info["loginname"],
		Email:     info["email"],
		Password:  info["password"],
		Salt:      info["salt"],
		Connected: boolstr(info["connected"]),
	}

	rooms, err := r.Client.SMembers("user-" + user.Type + "-" + user.ID.String() + "-rooms").Result()
	if err != nil {
		return nil, errors.Wrap(err, "r.Client.SMembers")
	}

	if len(rooms) > 0 {
		user.Rooms = rooms
	}

	groups, err := r.Client.SMembers("user-" + user.Type + "-" + user.ID.String() + "-groups").Result()
	if err != nil {
		return nil, errors.Wrap(err, "r.Client.SMembers")
	}

	if len(groups) > 0 {
		user.Groups = groups
	}

	return user, nil
}

func (r *rClient) getRoomData(gname, rname string) (*types.Room, error) {
	info, err := r.Client.HGetAllMap("room-" + gname + "-" + rname + "-info").Result()
	if err != nil {
		return nil, errors.Wrap(err, "r.Client.HGetAllMap")
	}

	room := &types.Room{
		Title:   info["title"],
		Group:   info["group"],
		Private: boolstr(info["private"]),
	}

	users, err := r.Client.SMembers("room-" + gname + "-" + rname + "-list").Result()
	if err != nil {
		return nil, errors.Wrap(err, "r.Client.SMembers")
	}

	room.Users = make(map[string]*types.User)
	if len(users) > 0 {
		for _, v := range users {
			u, err := r.getUserData(v)
			if err != nil {
				return nil, errors.Wrap(err, "r.getUserData")
			}
			room.Users[u.ID.String()] = u
		}
	}

	return room, nil
}

func (r *rClient) getGroupData(gname string) (*types.Group, error) {
	group := &types.Group{
		Title: gname,
		Rooms: make(map[string]*types.Room),
		Users: make(map[string]*types.User),
	}

	users, err := r.Client.SMembers("group-" + gname + "-users").Result()
	if err != nil {
		return nil, errors.Wrap(err, "r.Client.SMembers")
	}

	if len(users) > 0 {
		for _, v := range users {
			/* For some reason the length of this keeps coming up as 1 above
			 * the actual number of entries so confirm it's not nil before
			 * attempting to run GetUserData with the given string. */
			if v != "" {
				var u *types.User
				u, err = r.getUserData(v)
				if err != nil {
					return nil, errors.Wrap(err, "r.getUserData")
				}
				group.Users[u.ID.String()] = u
			}
		}
	}

	rooms, err := r.Client.SMembers("group-" + gname + "-rooms").Result()
	if err != nil {
		return nil, errors.Wrap(err, "r.Client.SMembers")
	}

	if len(rooms) > 0 {
		for _, v := range rooms {
			if v != "" {
				r, err := r.getRoomData(gname, v)
				if err != nil {
					return nil, errors.Wrap(err, "r.getRoomData")
				}
				group.Rooms[r.Title] = r
			}
		}
	}

	return group, nil
}

func (r *rClient) deleteUser(user *types.User) error {
	if err := r.Client.Del("user-" + user.Type + "-" + user.ID.String() + "-groups").Err(); err != nil {
		return errors.Wrap(err, "r.Client.Del")
	}
	if err := r.Client.Del("user-" + user.Type + "-" + user.ID.String() + "-rooms").Err(); err != nil {
		return errors.Wrap(err, "r.Client.Del")
	}
	if err := r.Client.Del("user-" + user.Type + "-" + user.LoginName + "-" + user.ID.String()).Err(); err != nil {
		return errors.Wrap(err, "r.Client.Del")
	}

	return nil
}

// GenRedisProto is an external function that can be used to generate Redis
// protocol text for mass insertion via CLI.
func GenRedisProto(cmd []string) string {
	var ret string
	ret = "*" + strconv.Itoa(len(cmd)) + "\r\n"
	for _, v := range cmd {
		ret += "$" + strconv.FormatInt(bytes.NewReader([]byte(v)).Size(), 10) + "\r\n"
		ret += v + "\r\n"
	}

	return ret
}
