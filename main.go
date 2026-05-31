package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-redis/redis"
	"github.com/sirupsen/logrus"
	"gopkg.in/telebot.v3"
)

var (
	telegramToken  = os.Getenv("TELEGRAM_TOKEN")
	redisURL       = os.Getenv("REDIS_URL")
	userServiceURL = "http://user-service/users"
	answersURL     = envOr("ANSWERS_URL", "http://answers/answers")
	webAdminURL    = envOr("WEB_ADMIN_URL", "http://web-admin/internal/token")
	redisClient    *redis.Client
	log            *logrus.Entry
	httpClient     = &http.Client{Timeout: 3 * time.Second}
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

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

	bot.Handle("/cabinet", handleCabinet)
	bot.Handle(telebot.OnText, handleIncomingText)
	bot.Handle(telebot.OnSticker, replyTextOnly)
	bot.Handle(telebot.OnVoice, replyTextOnly)
	bot.Handle(telebot.OnPhoto, replyTextOnly)
	bot.Handle(telebot.OnVideo, replyTextOnly)
	bot.Handle(telebot.OnAudio, replyTextOnly)
	bot.Handle(telebot.OnDocument, replyTextOnly)
	bot.Handle(telebot.OnAnimation, replyTextOnly)
	bot.Handle(telebot.OnVideoNote, replyTextOnly)

	go subscribeToRedisEvents(bot)

	log.Info("Starting telegram bot")
	bot.Start()
}

type answerReq struct {
	ChatID     int64  `json:"chat_id"`
	TelegramID int64  `json:"telegram_id"`
	Text       string `json:"text"`
	SentAt     string `json:"sent_at"`
}

func handleIncomingText(c telebot.Context) error {
	text := c.Text()
	if strings.HasPrefix(text, "/") {
		return nil // commands handled by their own handlers
	}

	req := answerReq{
		ChatID:     c.Chat().ID,
		TelegramID: c.Sender().ID,
		Text:       text,
		SentAt:     time.Now().UTC().Format(time.RFC3339),
	}

	if err := postAnswerWithRetry(req); err != nil {
		log.WithError(err).WithField("chat_id", req.ChatID).Error("Failed to forward answer")
		return c.Send("Не получилось сохранить, попробуйте позже")
	}
	return c.Send("Записал")
}

func replyTextOnly(c telebot.Context) error {
	return c.Send("Поддерживается только текст")
}

type cabinetResp struct {
	Link string `json:"link"`
}

func handleCabinet(c telebot.Context) error {
	body, _ := json.Marshal(map[string]int64{"chat_id": c.Chat().ID})
	resp, err := httpClient.Post(webAdminURL, "application/json", bytes.NewReader(body))
	if err != nil {
		log.WithError(err).WithField("chat_id", c.Chat().ID).Error("web-admin token request failed")
		return c.Send("Кабинет временно недоступен, попробуйте позже")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return c.Send("Кабинет временно недоступен, попробуйте позже")
	}
	var out cabinetResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Link == "" {
		return c.Send("Кабинет временно недоступен, попробуйте позже")
	}
	return c.Send(fmt.Sprintf("Ваш кабинет: %s\n(ссылка действует 15 минут)", out.Link))
}

func postAnswerWithRetry(req answerReq) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	for attempt := 1; attempt <= 2; attempt++ {
		resp, err := httpClient.Post(answersURL, "application/json", bytes.NewReader(body))
		if err != nil {
			if attempt == 2 {
				return err
			}
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if resp.StatusCode >= 500 && attempt == 1 {
			continue
		}
		return fmt.Errorf("answers POST failed: status %d", resp.StatusCode)
	}
	return fmt.Errorf("answers POST exhausted retries")
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
