package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"

	"github.com/go-redis/redis"
	"gopkg.in/telebot.v3"
)

var (
	telegramToken  = os.Getenv("TELEGRAM_TOKEN")
	redisURL       = os.Getenv("REDIS_URL")
	userServiceURL = "http://user-service/users"
	redisClient    *redis.Client
)

type User struct {
	ChatID       int64  `json:"chatID"`
	TelegramID   int64  `json:"telegramID"`
	FirstName    string `json:"firstName"`
	LastName     string `json:"lastName"`
	LanguageCode string `json:"languageCode"`
	Username     string `json:"username"`
}

func main() {
	if telegramToken == "" {
		log.Fatal("TELEGRAM_TOKEN is not set")
	}

	if redisURL == "" {
		redisURL = "localhost:6379"
	}

	redisClient = redis.NewClient(&redis.Options{
		Addr: redisURL,
	})

	_, err := redisClient.Ping().Result()
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}

	bot, err := telebot.NewBot(telebot.Settings{
		Token: telegramToken,
	})
	if err != nil {
		log.Fatal(err)
	}

	bot.Handle("/start", func(c telebot.Context) error {
		chatID := c.Chat().ID
		userExists, err := checkUserExists(chatID)
		if err != nil {
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
			err := createUser(user)
			if err != nil {
				return c.Send("Error creating user")
			}
		}

		return c.Send("Welcome!")
	})

	go subscribeToRedisEvents(bot)

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
			log.Printf("Redis subscription error: %v", err)
			continue
		}

		users, err := getAllUsers()
		if err != nil {
			log.Printf("Error fetching users: %v", err)
			continue
		}

		for _, user := range users {
			bot.Send(telebot.ChatID(user.ChatID), msg.Payload)
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

	err = json.Unmarshal(body, &users)
	if err != nil {
		return nil, err
	}

	return users, nil
}
