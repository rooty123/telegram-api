package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"

	"github.com/go-redis/redis"
	"github.com/sirupsen/logrus"
	"gopkg.in/telebot.v3"
)

var (
	telegramToken  = os.Getenv("TELEGRAM_TOKEN")
	redisURL       = os.Getenv("REDIS_URL")
	userServiceURL = "http://user-service/users"
	redisClient    *redis.Client
	log            *logrus.Entry
)

type User struct {
	ChatID       int64  `json:"chatID"`
	TelegramID   int64  `json:"telegramID"`
	FirstName    string `json:"firstName"`
	LastName     string `json:"lastName"`
	LanguageCode string `json:"languageCode"`
	Username     string `json:"username"`
}

func initLogger() {
	l := logrus.New()
	l.SetFormatter(&logrus.JSONFormatter{})
	if lvl, err := logrus.ParseLevel(os.Getenv("LOG_LEVEL")); err == nil {
		l.SetLevel(lvl)
	}
	name := os.Getenv("SERVICE_NAME")
	if name == "" {
		name = "telegram-api"
	}
	log = l.WithField("service_name", name)
}

func main() {
	initLogger()

	if telegramToken == "" {
		log.Fatal("TELEGRAM_TOKEN is not set")
	}

	if redisURL == "" {
		redisURL = "localhost:6379"
	}

	redisClient = redis.NewClient(&redis.Options{
		Addr: redisURL,
	})

	if _, err := redisClient.Ping().Result(); err != nil {
		log.WithError(err).Fatal("Failed to connect to Redis")
	}

	bot, err := telebot.NewBot(telebot.Settings{
		Token: telegramToken,
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to create telegram bot")
	}

	bot.Handle("/start", func(c telebot.Context) error {
		chatID := c.Chat().ID
		userExists, err := checkUserExists(chatID)
		if err != nil {
			log.WithError(err).WithField("chat_id", chatID).Error("Error checking user existence")
			return c.Send("Error checking user existence")
		}

		if !userExists {
			sender := c.Sender()
			user := User{
				ChatID:       chatID,
				TelegramID:   sender.ID,
				FirstName:    sender.FirstName,
				LastName:     sender.LastName,
				LanguageCode: sender.LanguageCode,
				Username:     sender.Username,
			}
			if err := createUser(user); err != nil {
				log.WithError(err).WithField("chat_id", chatID).Error("Error creating user")
				return c.Send("Error creating user")
			}
			log.WithField("chat_id", chatID).Info("New user created")
		}

		return c.Send("Welcome!")
	})

	go subscribeToRedisEvents(bot)

	log.Info("Starting telegram bot")
	bot.Start()
}

func checkUserExists(chatID int64) (bool, error) {
	resp, err := http.Get(fmt.Sprintf("%s/%d", userServiceURL, chatID))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	return resp.StatusCode == 200, nil
}

func createUser(user User) error {
	jsonData, err := json.Marshal(user)
	if err != nil {
		return err
	}

	resp, err := http.Post(userServiceURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("failed to create user, status: %d", resp.StatusCode)
	}

	return nil
}

func subscribeToRedisEvents(bot *telebot.Bot) {
	pubsub := redisClient.Subscribe("send_message")
	defer pubsub.Close()

	for {
		msg, err := pubsub.ReceiveMessage()
		if err != nil {
			log.WithError(err).Error("Redis subscription error")
			continue
		}

		users, err := getAllUsers()
		if err != nil {
			log.WithError(err).Error("Error fetching users")
			continue
		}

		for _, user := range users {
			if _, err := bot.Send(telebot.ChatID(user.ChatID), msg.Payload); err != nil {
				log.WithError(err).WithField("chat_id", user.ChatID).Error("Failed to send telegram message")
			}
		}
	}
}

func getAllUsers() ([]User, error) {
	resp, err := http.Get(userServiceURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var users []User
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(body, &users); err != nil {
		return nil, err
	}

	return users, nil
}
