package handlers

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	"tiberious/settings"
	"tiberious/types"

	"github.com/gorilla/websocket"
	"github.com/pborman/uuid"
)

var (
	config  types.Config
	clients = make(map[string]*types.Client)
)

func init() {
	config = settings.GetConfig()
}

// ClientHandler handles all client interactions
func ClientHandler(conn *websocket.Conn) {
	client := types.NewClient()
	client.Conn = conn

	/* TODO store UUID in a datastore of some sort (redis would work but
	 * database type should be configurable in datastore handler). */
	client.ID = uuid.NewRandom()

	clients[client.ID.String()] = client
	log.Println("client", client.ID, "connected")

	alert, err := json.Marshal(types.AlertMin{Response: 200, Time: time.Now().Unix()})
	if err != nil {
		/* Uncertain if this should be fatal or not, invalid
		 * operation on the server side should definitely cause
		 * some form of error presentation to the administrator
		 * but I'm uncertain about full shutdown. */
		log.Fatalln(err)
	}

	client.Conn.WriteMessage(websocket.BinaryMessage, alert)

	/* TODO we may want to remove this later it's just for easy testing.
	 * to allow a client to get their UUID back from the server after
	 * connecting. */
	info, err := json.Marshal(types.AlertFull{Response: 100, Time: time.Now().Unix(), Alert: string("Connected with ID " + client.ID.String())})
	if err != nil {
		/* TODO like the above and other places we need better error handling
		 * for this. */
		log.Fatalln(err)
	}

	client.Conn.WriteMessage(websocket.BinaryMessage, info)

	// Never return from this loop unless disconnecting the client...
	for {
		_, p, err := client.Conn.ReadMessage()
		if err != nil {
			log.Println(err)
			// TODO disconnect the client.
			return
		}

		var message types.MasterObj
		if err := json.Unmarshal(p, &message); err != nil {
			// TODO return 400, bad object.
			log.Println("invalid object from", client.ID.String(), ":", err)
			continue
		}

		if message.Time <= 0 {
			errfull, err := json.Marshal(types.ErrorFull{Response: 400, Time: time.Now().Unix(), Error: "missing or invalid time"})
			if err != nil {
				/* TODO implement better internal error handling in case JSON
				 * marshalling fails for some reason. */
				log.Fatalln(err)
			}

			client.Conn.WriteMessage(websocket.BinaryMessage, errfull)
			log.Println("returned", string(errfull), "to client", client.ID.String())
			continue
		}

		switch {
		case message.Action == "msg":
			/* TODO parse the destination and if the destination exists
			 * send the message (should work for 1to1 even if the user is
			 * not currently online (with databasing enabled, otherwise should
			 * return an error)); if destination doesn't exist return an
			 * error (for now just return the message itself for testing).
			 */

			switch {
			// All room's start with "#"
			case strings.HasPrefix(message.To, "#"):
				/* TODO we don't want to stop people from outside of a room from
				 * messaging that room directly unless it's a private channel
				 * (these don't exist yet but should restrict to only members
				 * of said channel) */
				rexists, room := GetRoom(message.To)
				if !rexists {
					errmin, err := json.Marshal(types.ErrorMin{Response: 404, Time: time.Now().Unix()})
					if err != nil {
						// LOGGING
						log.Fatalln(err)
					}

					client.Conn.WriteMessage(websocket.BinaryMessage, errmin)
					continue
				}
				// TODO should this be handled in a channel or goroutine?
				for _, c := range room {
					c.Conn.WriteMessage(websocket.BinaryMessage, p)
				}
			default:
				// Handle 1to1 messaging.

				/* TODO handle server side message logging. handle an error
				 * message for non-existing users (requires user database)
				 * and a separate one for users not being logged on. */

				var relayed = false
				for k, c := range clients {
					if message.To == k {
						c.Conn.WriteMessage(websocket.BinaryMessage, p)
						relayed = true
					}
				}

				if relayed {
					break
				}

				errmin, err := json.Marshal(types.ErrorMin{Response: 404, Time: time.Now().Unix()})
				if err != nil {
					// TODO afforementioned logging/error handling.
					log.Fatalln(err)
				}

				client.Conn.WriteMessage(websocket.BinaryMessage, errmin)
				log.Println("returned", string(errmin), "to client", client.ID.String())
				continue
			}

			// Send a response back saying the message was sent.
			alertmin, err := json.Marshal(types.AlertMin{Response: 200, Time: time.Now().Unix()})
			if err != nil {
				// TODO this needs to be replaced with proper logging/handling.
				log.Fatalln(err)
			}

			client.Conn.WriteMessage(websocket.BinaryMessage, alertmin)

			break
		case message.Action == "join":
			// TODO implement private rooms
			var rexists = false
			var room types.Room
			rexists, room = GetRoom(message.Room)
			if !rexists {
				room = GetNewRoom(message.Room)
			}

			room[client.ID.String()] = client
			// Send a response back confirming we joined the room..
			alertmin, err := json.Marshal(types.AlertMin{Response: 200, Time: time.Now().Unix()})
			if err != nil {
				// TODO this needs to be replaced with proper logging/handling.
				log.Fatalln(err)
			}

			client.Conn.WriteMessage(websocket.BinaryMessage, alertmin)
			break
		case message.Action == "leave":
		case message.Action == "part":
			var rexists = false
			var room types.Room
			rexists, room = GetRoom(message.Room)
			if !rexists {
				errmin, err := json.Marshal(types.ErrorMin{Response: 404, Time: time.Now().Unix()})
				if err != nil {
					log.Fatalln(err)
				}
				client.Conn.WriteMessage(websocket.BinaryMessage, errmin)
				log.Println("return", string(errmin), "to client", client.ID.String())
				break
			}

			var ispresent = false
			for k := range room {
				if k == client.ID.String() {
					ispresent = true
					break
				}
			}

			if !ispresent {
				// TODO should this return a different error number?
				errmin, err := json.Marshal(types.ErrorMin{Response: 410, Time: time.Now().Unix()})
				if err != nil {
					log.Fatalln(err)
				}
				client.Conn.WriteMessage(websocket.BinaryMessage, errmin)
				log.Println("returned", string(errmin), "to client", client.ID.String())
				break
			}

			delete(room, client.ID.String())

			// Send a response back confirming we left the room..
			alertmin, err := json.Marshal(types.AlertMin{Response: 200, Time: time.Now().Unix()})
			if err != nil {
				// TODO this needs to be replaced with proper logging/handling.
				log.Fatalln(err)
			}

			client.Conn.WriteMessage(websocket.BinaryMessage, alertmin)
			break
		default:
			errmin, err := json.Marshal(types.ErrorMin{Response: 400, Time: time.Now().Unix()})
			if err != nil {
				/* Uncertain if this should be fatal or not, invalid
				 * operation on the server side should definitely cause
				 * some form of error presentation to the administrator
				 * but I'm uncertain about full shutdown. */
				log.Fatalln(err)
			}
			client.Conn.WriteMessage(websocket.BinaryMessage, errmin)
			log.Println("returned", string(errmin), "to client", client.ID.String())
			break
		}
	}
}
