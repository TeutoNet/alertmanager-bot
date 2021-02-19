package workflows

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/metalmatze/alertmanager-bot/pkg/telegram"
	"github.com/prometheus/alertmanager/notify"
	"github.com/stretchr/testify/require"
	"github.com/tucnak/telebot"
)

var (
	admin = telebot.User{
		ID:        123,
		FirstName: "Elliot",
		LastName:  "Alderson",
		Username:  "elliot",
		IsBot:     false,
	}
	nobody = telebot.User{
		ID:        222,
		FirstName: "John",
		LastName:  "Doe",
		Username:  "nobody",
		IsBot:     false,
	}

	// These are the different workflows/scenarios we are testing.
	workflows = []struct {
		name     string
		messages []telebot.Message
		replies  []testTelegramReply
		logs     []string
	}{{
		name: "Dropped",
		messages: []telebot.Message{{
			Sender: nobody,
		}},
		replies: []testTelegramReply{},
		logs: []string{
			"level=info msg=\"failed to process message\" err=\"dropped message from forbidden sender\" sender_id=222 sender_username=nobody",
		},
	}, {
		name: "Incomprehensible",
		messages: []telebot.Message{{
			Sender: admin,
			Text:   "/incomprehensible",
			Chat:   chatFromUser(admin),
		}},
		replies: []testTelegramReply{{
			recipient: "123",
			message:   "Sorry, I don't understand...",
		}},
		logs: []string{
			"level=debug msg=\"message received\" text=/incomprehensible",
		},
	}, {
		name: "Start",
		messages: []telebot.Message{{
			Sender: admin,
			Text:   telegram.CommandStart,
			Chat:   chatFromUser(admin),
		}},
		replies: []testTelegramReply{{
			recipient: "123",
			message:   "Hey, Elliot! I will now keep you up to date!\n/help",
		}},
		logs: []string{
			"level=debug msg=\"message received\" text=/start",
			"level=info msg=\"user subscribed\" username=elliot user_id=123",
		},
	}, {
		name: "StopWithoutStart",
		messages: []telebot.Message{{
			Sender: admin,
			Text:   telegram.CommandStop,
			Chat:   chatFromUser(admin),
		}},
		replies: []testTelegramReply{{
			recipient: "123",
			message:   "Alright, Elliot! I won't talk to you again.\n/help",
		}},
		logs: []string{
			"level=debug msg=\"message received\" text=/stop",
			"level=info msg=\"user unsubscribed\" username=elliot user_id=123",
		},
	}, {
		name: "Help",
		messages: []telebot.Message{{
			Sender: admin,
			Text:   telegram.CommandHelp,
			Chat:   chatFromUser(admin),
		}},
		replies: []testTelegramReply{{
			recipient: "123",
			message:   telegram.ResponseHelp,
		}},
		logs: []string{
			"level=debug msg=\"message received\" text=/help",
		},
	}, {
		name: "HelpAsNobody",
		messages: []telebot.Message{{
			Sender: nobody,
			Text:   telegram.CommandHelp,
			Chat:   chatFromUser(nobody),
		}},
		replies: []testTelegramReply{},
		logs: []string{
			"level=info msg=\"failed to process message\" err=\"dropped message from forbidden sender\" sender_id=222 sender_username=nobody",
		},
	}, {
		name: "ChatsNone",
		messages: []telebot.Message{{
			Sender: admin,
			Text:   telegram.CommandChats,
			Chat:   chatFromUser(admin),
		}},
		replies: []testTelegramReply{{
			recipient: "123",
			message:   "Currently no one is subscribed.",
		}},
		logs: []string{
			"level=debug msg=\"message received\" text=/chats",
		},
	}, {
		name: "ChatsWithAdminSubscribed",
		messages: []telebot.Message{{
			Sender: admin,
			Text:   telegram.CommandStart,
			Chat:   chatFromUser(admin),
		}, {
			Sender: admin,
			Text:   telegram.CommandChats,
			Chat:   chatFromUser(admin),
		}},
		replies: []testTelegramReply{{
			recipient: "123",
			message:   "Hey, Elliot! I will now keep you up to date!\n/help",
		}, {
			recipient: "123",
			message:   "Currently these chat have subscribed:\n@elliot\n",
		}},
		logs: []string{
			"level=debug msg=\"message received\" text=/start",
			"level=info msg=\"user subscribed\" username=elliot user_id=123",
			"level=debug msg=\"message received\" text=/chats",
		},
	}}
)

func chatFromUser(user telebot.User) telebot.Chat {
	return telebot.Chat{
		ID:        int64(user.ID),
		FirstName: user.FirstName,
		LastName:  user.LastName,
		Username:  user.Username,
		Type:      telebot.ChatPrivate,
	}
}

type testStore struct {
	// not thread safe - lol
	chats map[int64]telebot.Chat
}

func (t *testStore) List() ([]telebot.Chat, error) {
	chats := make([]telebot.Chat, 0, len(t.chats))
	for _, chat := range t.chats {
		chats = append(chats, chat)
	}
	return chats, nil
}

func (t *testStore) Add(c telebot.Chat) error {
	if t.chats == nil {
		t.chats = make(map[int64]telebot.Chat)
	}
	t.chats[c.ID] = c
	return nil
}

func (t *testStore) Remove(_ telebot.Chat) error {
	return nil
}

type testTelegramReply struct {
	recipient, message string
}

type testTelegram struct {
	messages []telebot.Message
	replies  []testTelegramReply
}

func (t *testTelegram) Listen(messages chan telebot.Message, _ time.Duration) {
	for i, m := range t.messages {
		m.ID = i
		messages <- m
	}
}

func (t *testTelegram) SendChatAction(_ telebot.Recipient, _ telebot.ChatAction) error {
	return nil
}

func (t *testTelegram) SendMessage(recipient telebot.Recipient, message string, _ *telebot.SendOptions) error {
	t.replies = append(t.replies, testTelegramReply{recipient: recipient.Destination(), message: message})
	return nil
}

func TestWorkflows(t *testing.T) {
	for _, w := range workflows {
		t.Run(w.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			logs := &bytes.Buffer{}

			testStore := &testStore{}
			testTelegram := &testTelegram{messages: w.messages}

			bot, err := telegram.NewBotWithTelegram(testStore, testTelegram, admin.ID,
				telegram.WithLogger(log.NewLogfmtLogger(logs)),
			)
			require.NoError(t, err)

			// Run the bot in the background and tests in foreground.
			go func(ctx context.Context) {
				err = bot.Run(ctx, make(chan notify.WebhookMessage))
				require.NoError(t, err)
			}(ctx)

			// TODO: Don't sleep but block somehow different
			time.Sleep(100 * time.Millisecond)

			require.Len(t, testTelegram.replies, len(w.replies))
			for i, reply := range w.replies {
				require.Equal(t, reply.recipient, testTelegram.replies[i].recipient)
				require.Equal(t, reply.message, testTelegram.replies[i].message)
			}

			logLines := strings.Split(strings.TrimSpace(logs.String()), "\n")

			require.Len(t, logLines, len(w.logs))
			for i, l := range w.logs {
				require.Equal(t, l, logLines[i])
			}

			cancel()
		})
	}
}
