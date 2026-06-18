package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"math/rand"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
	"github.com/valyala/fasthttp"
)

type TokenConfig struct {
	Token   string
	IsMain  bool
	Session *discordgo.Session
}

type NitroCode struct {
	Code             string
	Source           string // Which token found it
	Timestamp        time.Time
	MessageTimestamp time.Time
	DetectLag        time.Duration
	AuthorUsername   string // Who sent the message with the code
	AuthorID         string // ID of who sent the message
	ChannelID        string // Channel where code was found
	GuildID          string // Guild where code was found
}

// Config holds configurable parameters for safe operation
type Config struct {
	// Rate limiting and delays
	MinClaimDelay    time.Duration // Minimum delay before claiming a code
	MaxClaimDelay    time.Duration // Maximum delay before claiming a code
	AltConnectDelay  time.Duration // Delay between connecting alt accounts
	ChannelRefreshInterval time.Duration // How often to refresh channel subscriptions
	
	// Connection limits
	MaxConnsPerHost int // Max connections per host to appear more human-like
	MaxConcurrentConnections int // Max concurrent connections
	
	// Safety settings
	MaxAccounts int // Maximum number of accounts to use
	UseProxies bool // Whether to use proxy connections
	RandomizeUserAgent bool // Whether to randomize user agents
	RandomizeDelays bool // Whether to randomize delays
	SpeedMode bool // When true, minimizes delays for maximum speed (higher risk of detection)
}

// DefaultConfig returns a safe default configuration
func DefaultConfig() *Config {
	return &Config{
		MinClaimDelay: 100 * time.Millisecond,
		MaxClaimDelay: 500 * time.Millisecond,
		AltConnectDelay: 3000 * time.Millisecond,
		ChannelRefreshInterval: 60 * time.Minute,
		MaxConnsPerHost: 50,
		MaxConcurrentConnections: 1024,
		MaxAccounts: 3,
		UseProxies: false,
		RandomizeUserAgent: true,
		RandomizeDelays: true,
		SpeedMode: true, // Default to speed mode
	}
}

var (
	mainToken           string
	altTokens           []string
	mainSession         *discordgo.Session
	altSessions         []*discordgo.Session
	webhookURL          string
	nitroRegex          = regexp.MustCompile(`(?mi)(?:https?://)?(?:ptb\.\|canary\.)?(?:discord(?:app)?\.com/(?:gifts|billing/promotions)|discord\.gift)/([a-zA-Z0-9]{16,32})`)
	totalCodes          int64
	totalClaims         int64
	totalClaimAttempts  int64
	totalUnknownCodes   int64
	totalAlreadyClaimed int64
	recentCodesMu       sync.Mutex
	recentCodes         = make(map[string]time.Time, 4096)
	recentCodeTTL       = 2 * time.Minute

	// Additional regex patterns to catch various nitro code formats
	oldNitroRegex       = regexp.MustCompile(`discord\.gift/([a-zA-Z0-9]{16,32})`)
	fullUrlNitroRegex   = regexp.MustCompile(`discordapp\.com/gifts/([a-zA-Z0-9]{16,32})`)
	newNitroRegex       = regexp.MustCompile(`(?mi)(?:https?://)?(?:ptb\.|canary\.)?(?:discord(?:app)?\.com/(?:gifts|billing/promotions)|discord\.gift)/([a-zA-Z0-9]{16,26})`)

	// Debug mode flag - set to false to disable debug logs
	debugMode           = false

	// Channel scanning flag - set to false to disable automatic channel scanning
	channelScanningEnabled = true
	
	// Channel scanning logging flag - set to false to disable logging of channel scanning
	channelScanningLogging = false
	
	// Channel refresh logging flag - set to false to disable logging of channel refresh
	channelRefreshLogging = true

	// Configuration
	config = DefaultConfig()

	// Global optimized HTTP client for reuse
	globalHTTPClient = &fasthttp.Client{
		ReadTimeout:                   10 * time.Second,
		WriteTimeout:                  10 * time.Second,
		MaxIdleConnDuration:           5 * time.Minute,
		MaxConnsPerHost:               config.MaxConnsPerHost,
		MaxResponseBodySize:           1024 * 1024,
		MaxIdemponentCallAttempts:     2, // Reduced attempts
		DisableHeaderNamesNormalizing: true,
		DisablePathNormalizing:        true,
		Dial: (&fasthttp.TCPDialer{
			Concurrency:      config.MaxConcurrentConnections,
			DNSCacheDuration: time.Hour,
		}).Dial,
	}

	// Reuse one webhook client instead of allocating one per notification.
	webhookClient = &fasthttp.Client{
		ReadTimeout:                   10 * time.Second,
		WriteTimeout:                  10 * time.Second,
		MaxIdleConnDuration:           5 * time.Minute,
		MaxConnsPerHost:               config.MaxConnsPerHost,
		MaxResponseBodySize:           1024 * 1024,
		MaxIdemponentCallAttempts:     2, // Reduced attempts
		DisableHeaderNamesNormalizing: true,
		DisablePathNormalizing:        true,
		Dial: (&fasthttp.TCPDialer{
			Concurrency:      config.MaxConcurrentConnections/4, // Reduced concurrency
			DNSCacheDuration: time.Hour,
		}).Dial,
	}

	sessionConnectRetries = 2 // Reduced retries to appear more natural
)

func init() {
	// Seed random number generator for delays and user-agents
	rand.Seed(time.Now().UnixNano())
	
	// Load environment variables
	_ = godotenv.Load()

	// Load tokens and webhook from environment or file
	loadConfiguration()
}

func loadConfiguration() {
	// Try loading from environment first
	mainToken = normalizeToken(os.Getenv("MAIN_TOKEN"))
	webhookURL = os.Getenv("WEBHOOK_URL")
	
	// Check for speed mode from environment
	if os.Getenv("SPEED_MODE") != "" {
		config.SpeedMode = strings.ToLower(os.Getenv("SPEED_MODE")) == "true" || os.Getenv("SPEED_MODE") == "1" || strings.ToLower(os.Getenv("SPEED_MODE")) == "yes"
	}

	// Load alt tokens from environment variables (ALT_TOKEN_1, ALT_TOKEN_2, etc.)
	for i := 1; ; i++ {
		envVar := fmt.Sprintf("ALT_TOKEN_%d", i)
		token := normalizeToken(os.Getenv(envVar))
		if token == "" {
			break
		}
		altTokens = append(altTokens, token)
	}

	// If not in environment, try reading from tokens.txt
	if mainToken == "" || len(altTokens) == 0 {
		file, err := os.Open("tokens.txt")
		if err != nil {
			errorMsg := "No tokens.txt file found and environment variables not set"
			log.Println("" + errorMsg)
			sendWebhook("CRITICAL ERROR: " + errorMsg)
			os.Exit(1) // Exit instead of using log.Fatal
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		altIndex := 0
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			if strings.Contains(line, "=") {
				parts := strings.SplitN(line, "=", 2)
				key := strings.TrimSpace(parts[0])
				value := normalizeToken(parts[1])

				if key == "MAIN_TOKEN" {
					mainToken = value
				} else if key == "WEBHOOK_URL" {
					webhookURL = strings.Trim(strings.TrimSpace(parts[1]), `"'`)
				} else if strings.HasPrefix(key, "ALT_TOKEN_") {
					altTokens = append(altTokens, value)
				} else if key == "SPEED_MODE" {
					// Enable speed mode if SPEED_MODE is set to true/1/yes
					config.SpeedMode = strings.ToLower(value) == "true" || value == "1" || strings.ToLower(value) == "yes"
				}
			} else {
				// Handle tokens without prefixes for backward compatibility
				line = normalizeToken(line)
				if altIndex == 0 && mainToken == "" {
					mainToken = line
				} else if mainToken != "" && line != mainToken {
					altTokens = append(altTokens, line)
				}
				altIndex++
			}
		}

		if mainToken == "" {
			errorMsg := "No main token provided"
			log.Println("" + errorMsg)
			sendWebhook("CRITICAL ERROR: " + errorMsg)
			os.Exit(1) // Exit instead of using log.Fatal
		}
	}

	altTokens = dedupeAndFilterAltTokens(altTokens, mainToken)
	if len(altTokens) == 0 {
		log.Println("Warning: no usable alt tokens loaded")
	}

	log.Printf("Loaded %d alt tokens", len(altTokens))
	log.Printf("Speed Mode: %t", config.SpeedMode)

	// Send startup webhook notification
	go sendWebhook(fmt.Sprintf("King Sniper Started!\nLoaded %d alt tokens\nSpeed Mode: %t", len(altTokens), config.SpeedMode))
}

func normalizeToken(raw string) string {
	token := strings.TrimSpace(strings.TrimPrefix(raw, "\uFEFF"))
	token = strings.Trim(token, `"'`)
	if token == "" {
		return ""
	}

	lower := strings.ToLower(token)
	if strings.Count(token, ":") >= 2 &&
		!strings.HasPrefix(lower, "bot ") &&
		!strings.HasPrefix(lower, "bearer ") {
		parts := strings.Split(token, ":")
		token = strings.TrimSpace(parts[len(parts)-1])
	}

	return strings.Trim(token, `"'`)
}

func dedupeAndFilterAltTokens(tokens []string, main string) []string {
	filtered := make([]string, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens)+1)

	if main != "" {
		seen[main] = struct{}{}
	}

	for _, token := range tokens {
		token = normalizeToken(token)
		if token == "" {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		filtered = append(filtered, token)
	}

	return filtered
}

func configureSession(dg *discordgo.Session) {
	// For selfbots, we still need to set certain intents to access message content
	// Though selfbots have more permissions than regular bots, we still need to specify what we want to receive
	dg.Identify.Intents = discordgo.MakeIntent(
		discordgo.IntentsGuilds |
			discordgo.IntentsGuildMessages |
			discordgo.IntentsDirectMessages |
			discordgo.IntentsMessageContent,
	)
	dg.ShouldReconnectOnError = true
	dg.MaxRestRetries = 3
	
	// For user accounts, we need to ensure we're getting all possible message events
	// by subscribing to presence and message events across all accessible channels
}

func tokenVariants(token string) []string {
	token = normalizeToken(token)
	if token == "" {
		return nil
	}

	variants := []string{token}
	lower := strings.ToLower(token)
	if !strings.HasPrefix(lower, "bot ") && !strings.HasPrefix(lower, "bearer ") {
		variants = append(variants, "Bot "+token)
	}

	return variants
}

func connectSessionWithRetries(token string, handler interface{}) (*discordgo.Session, error) {
	var lastErr error

	for attempt := 1; attempt <= sessionConnectRetries; attempt++ {
		for _, variant := range tokenVariants(token) {
			// Add slight delay between connection attempts to appear more human-like
			if attempt > 1 && !config.SpeedMode {
				time.Sleep(time.Duration(rand.Intn(2000)) * time.Millisecond) // Random delay 0-2 seconds
			}
			
			dg, err := discordgo.New(variant)
			if err != nil {
				lastErr = err
				continue
			}

			// Configure for selfbot - different from bot behavior
			configureSession(dg)
			
			// For selfbot, we need to set the correct properties to appear as a user client
			dg.Identify.Compress = false
			dg.Identify.LargeThreshold = 0
			
			// Set the device to appear as a normal Discord client
			// Use randomized properties to avoid detection patterns
			dg.Identify.Properties.Device = ""
			dg.Identify.Properties.Browser = getBrowserVariant()
			dg.Identify.Properties.OS = getOSVariant()
			
			if handler != nil {
				dg.AddHandler(handler)
			}

			err = dg.Open()
			if err == nil {
				return dg, nil
			}

			lastErr = err
		}

		if attempt < sessionConnectRetries {
			if !config.SpeedMode {
				time.Sleep(time.Duration(attempt+rand.Intn(3)) * time.Second) // Randomized retry delay
			}
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no valid token variants available")
	}

	return nil, lastErr
}

func getBrowserVariant() string {
	browsers := []string{"Discord Client", "Discord", "Electron", "Chrome"}
	return browsers[rand.Intn(len(browsers))]
}

func getOSVariant() string {
	systems := []string{"Windows", "Linux", "Darwin", "Windows NT"}
	return systems[rand.Intn(len(systems))]
}

func main() {
	log.Println("King Sniper - World's Fastest Nitro Sniper Starting...")

	// Start stats reporter
	go reportStats()

	// Connect main token
	log.Println("Connecting main token...")
	connectMainToken()

	// Connect alt tokens
	log.Println("Connecting", len(altTokens), "alt tokens...")
	connectAltTokens()

	// Periodically refresh channel subscriptions to ensure continued message reception
	go refreshChannelSubscriptions()

	// Keep the program running
	select {}
}

// Periodically refresh channel subscriptions to ensure continued message reception
func refreshChannelSubscriptions() {
	ticker := time.NewTicker(config.ChannelRefreshInterval) // Refresh interval from config
	defer ticker.Stop()

	for range ticker.C {
		if channelScanningEnabled {
			if channelRefreshLogging {
				log.Println("Refreshing channel subscriptions...")
			}
			if mainSession != nil {
				go subscribeToAllChannels(mainSession)
			}
			for _, session := range altSessions {
				if session != nil {
					if !config.SpeedMode {
						time.Sleep(2 * time.Second) // Add delay between alt sessions to appear more human-like
					}
					go subscribeToAllChannels(session)
				}
			}
			if channelRefreshLogging {
				log.Println("Completed channel subscription refresh")
			}
		}
	}
}

func connectMainToken() {
	dg, err := connectSessionWithRetries(mainToken, messageCreateMain)
	if err != nil {
		log.Println("Failed to connect main token:", err)
		sendWebhook(fmt.Sprintf("FAILED to connect main token\n Error: %v", err))
		os.Exit(1) // Exit instead of using log.Fatal
	}
	mainSession = dg

	log.Println("Main token connected and ready!")

	// Send success webhook
	go sendWebhook("Main token connected and ready!")
	
	// Subscribe to all channels to ensure message events are received (if channel scanning is enabled)
	if channelScanningEnabled {
		go subscribeToAllChannels(mainSession)
	}
}

func connectAltTokens() {
	connected := 0
	for i, token := range altTokens {
		dg, err := connectSessionWithRetries(token, messageCreateAlt)
		if err != nil {
			log.Printf("Failed to connect alt token %d: %v", i+1, err)
			go sendWebhook(fmt.Sprintf("FAILED to connect alt token %d\n Error: %v", i+1, err))
			if !config.SpeedMode {
				time.Sleep(config.AltConnectDelay)
			}
			continue
		}

		altSessions = append(altSessions, dg)
		connected++
		log.Printf("Alt token %d connected and ready!", i+1)
		go sendWebhook(fmt.Sprintf("Alt token %d connected and ready!", i+1))
		
		// Subscribe to all channels to ensure message events are received (if channel scanning is enabled)
		if channelScanningEnabled {
			go subscribeToAllChannels(dg)
		}

		// Space out IDENTIFYs to avoid gateway throttling on large alt lists.
		if !config.SpeedMode {
			time.Sleep(config.AltConnectDelay)
			time.Sleep(time.Duration(rand.Intn(2000)) * time.Millisecond) // Add random extra delay to appear more human-like
		}
	}

	log.Printf("Alt tokens connected: %d/%d", connected, len(altTokens))
	if connected == 0 {
		go sendWebhook("FAILED to connect any alt tokens")
	}
}

func messageCreateMain(s *discordgo.Session, m *discordgo.MessageCreate) {
	go processIncomingMessage(s, m)
}

func messageCreateAlt(s *discordgo.Session, m *discordgo.MessageCreate) {
	go processIncomingMessage(s, m)
}

func processIncomingMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	source := sessionSourceName(s)
	now := time.Now()
	messageTS := messageTimestamp(m)
	detectLag := time.Duration(0)
	if !messageTS.IsZero() && now.After(messageTS) {
		detectLag = now.Sub(messageTS)
	}
	
	// Small random delay to prevent rapid processing that looks bot-like, unless in speed mode
	if !config.SpeedMode {
		time.Sleep(time.Duration(rand.Intn(50)) * time.Millisecond) // 0-50ms delay
	}
	
	// Check if the message is from the bot itself to avoid processing own messages
	isOwnMessage := false
	if s != nil && s.State != nil && s.State.User != nil {
		isOwnMessage = m.Author.ID == s.State.User.ID
	}
	
	if isOwnMessage {
		if debugMode {
			log.Printf("[DEBUG] Skipping own message from %s (ID: %s)", m.Author.Username, m.Author.ID)
		}
		return // Don't process own messages
	}
	
	if debugMode {
		log.Printf("[DEBUG] Received message from %s (ID: %s) in channel %s, content: %s", m.Author.Username, m.Author.ID, m.ChannelID, m.Content)
	}
	
	// Debug: Check if message contains potential nitro codes
	if debugMode {
		debugLog := ""
		if m.Content != "" {
			if strings.Contains(m.Content, "discord.gift") || strings.Contains(m.Content, "discord.com/gifts") {
				debugLog += fmt.Sprintf("[DEBUG] Message from %s contains potential nitro gift: %s", source, m.Content)
			}
		}
		for _, embed := range m.Embeds {
			if embed != nil && (strings.Contains(embed.URL, "discord.gift") || strings.Contains(embed.URL, "discord.com/gifts")) {
				debugLog += fmt.Sprintf("[DEBUG] Message from %s contains embed with potential nitro gift: %s", source, embed.URL)
			}
		}
		if debugLog != "" {
			log.Println(debugLog)
		}
	}

	for _, code := range findNitroCodes(m) {
		if !markCodeIfNew(code, now) {
			continue
		}

		atomic.AddInt64(&totalCodes, 1)
		nitroCode := NitroCode{
			Code:             code,
			Source:           source,
			Timestamp:        now,
			MessageTimestamp: messageTS,
			DetectLag:        detectLag,
			AuthorUsername:   m.Author.Username,
			AuthorID:         m.Author.ID,
			ChannelID:        m.ChannelID,
			GuildID:          m.GuildID,
		}

		ping1 := getSessionPing(s)
		log.Printf("[Nitro] [%s] Found code: %s at %s detect_lag_ms=%d ping=%s", source, code, now.Format("15:04:05.000"), detectLag.Milliseconds(), ping1)

		// Add small delay before claiming to make it appear more human-like, unless in speed mode
		if !config.SpeedMode {
			time.Sleep(time.Duration(rand.Intn(100)) * time.Millisecond) // 0-100ms delay before claiming
		}
		
		// Claim immediately from the handler goroutine to avoid queue hop latency.
		claimWithMainToken(nitroCode)

		// Add delay before sending webhook notification to make it appear more human-like, unless in speed mode
		if !config.SpeedMode {
			time.Sleep(time.Duration(rand.Intn(100)) * time.Millisecond) // 0-100ms delay before webhook
		}
		
		// Notifications stay async and out of the claim path.
		ping2 := getSessionPing(s)
		go sendWebhook(fmt.Sprintf("[Nitro] [%s] Spotted code: `%s`\nTime: %s\nPing: %s\nSender: %s (%s)\nChannel: %s\nGuild: %s", source, code, now.Format("15:04:05.000"), ping2, m.Author.Username, m.Author.ID, m.ChannelID, m.GuildID))
	}
}

func sessionSourceName(s *discordgo.Session) string {
	if s != nil && s.State != nil && s.State.User != nil && s.State.User.Username != "" {
		return s.State.User.Username
	}
	return "unknown-session"
}

func getSessionPing(s *discordgo.Session) string {
	if s == nil {
		return "N/A"
	}
	latency := s.HeartbeatLatency()
	if latency <= 0 {
		return "N/A"
	}
	return fmt.Sprintf("%d ms", latency.Milliseconds())
}

func messageTimestamp(m *discordgo.MessageCreate) time.Time {
	if m == nil || m.Message == nil {
		return time.Time{}
	}
	return m.Timestamp
}

func findNitroCodes(m *discordgo.MessageCreate) []string {
	if m == nil || m.Message == nil {
		if debugMode {
			log.Printf("[DEBUG] Message is nil, skipping")
		}
		return nil
	}
	if debugMode {
		log.Printf("[DEBUG] Processing message from author: %s, content length: %d", m.Author.Username, len(m.Content))
	}

	candidates := make([]string, 0, 1+len(m.Embeds)*6)
	if m.Content != "" {
		candidates = append(candidates, m.Content)
		if debugMode {
			log.Printf("[DEBUG] Checking message content: %s", m.Content)
		}
	} else {
		if debugMode {
			log.Printf("[DEBUG] Message content is empty")
		}
	}

	for _, embed := range m.Embeds {
		if embed == nil {
			continue
		}

		if embed.URL != "" {
			candidates = append(candidates, embed.URL)
		}
		if embed.Title != "" {
			candidates = append(candidates, embed.Title)
		}
		if embed.Description != "" {
			candidates = append(candidates, embed.Description)
		}
		if embed.Footer != nil && embed.Footer.Text != "" {
			candidates = append(candidates, embed.Footer.Text)
		}
		if embed.Author != nil {
			if embed.Author.Name != "" {
				candidates = append(candidates, embed.Author.Name)
			}
			if embed.Author.URL != "" {
				candidates = append(candidates, embed.Author.URL)
			}
		}
		for _, field := range embed.Fields {
			if field == nil {
				continue
			}
			if field.Name != "" {
				candidates = append(candidates, field.Name)
			}
			if field.Value != "" {
				candidates = append(candidates, field.Value)
			}
		}
	}

	seen := make(map[string]struct{})
	codes := make([]string, 0, 2)

	for _, text := range candidates {
		// Check if the text contains any known discord gift patterns first
		if strings.Contains(text, "discord.gift") || strings.Contains(text, "discord.com/gifts") {
			if debugMode {
				log.Printf("[DEBUG] Found potential discord gift URL in text: %s", text)
			}
		}
		
		// Try multiple regex patterns to catch various nitro code formats
		allMatches := [][]string{}
		
		// Original regex
		matches1 := nitroRegex.FindAllStringSubmatch(text, -1)
		allMatches = append(allMatches, matches1...)
		
		// Additional regex patterns
		matches2 := oldNitroRegex.FindAllStringSubmatch(text, -1)
		allMatches = append(allMatches, matches2...)
		
		matches3 := fullUrlNitroRegex.FindAllStringSubmatch(text, -1)
		allMatches = append(allMatches, matches3...)
		
		matches4 := newNitroRegex.FindAllStringSubmatch(text, -1)
		allMatches = append(allMatches, matches4...)

		if debugMode {
			log.Printf("[DEBUG] Processing text: %s, found %d potential matches with all regex patterns", text, len(allMatches))
		}
		
		for _, match := range allMatches {
			if len(match) < 2 {
				continue
			}

			code := strings.TrimSpace(match[1])
			if debugMode {
				log.Printf("[DEBUG] Extracted code: %s, length: %d", code, len(code))
			}
			
			// Validate code length (between 16 and 32 characters)
			if len(code) < 16 || len(code) > 32 {
				if debugMode {
					log.Printf("[DEBUG] Code %s rejected: length %d is not between 16 and 32", code, len(code))
				}
				continue
			}
			
			if _, exists := seen[code]; exists {
				if debugMode {
					log.Printf("[DEBUG] Code %s already seen in this message", code)
				}
				continue
			}

			seen[code] = struct{}{}
			codes = append(codes, code)
			if debugMode {
				log.Printf("[DEBUG] Added code %s to codes list", code)
			}
		}
	}

	if debugMode {
		log.Printf("[DEBUG] Total codes found in message: %d", len(codes))
	}
	return codes
}

func markCodeIfNew(code string, now time.Time) bool {
	recentCodesMu.Lock()
	defer recentCodesMu.Unlock()

	if lastSeen, exists := recentCodes[code]; exists && now.Sub(lastSeen) < recentCodeTTL {
		if debugMode {
			log.Printf("[DEBUG] Code %s was already seen recently at %v, skipping", code, lastSeen)
		}
		return false
	}
	recentCodes[code] = now

	// Opportunistic cleanup to bound memory usage.
	if len(recentCodes) > 10000 {
		cutoff := now.Add(-recentCodeTTL)
		for existingCode, seenAt := range recentCodes {
			if seenAt.Before(cutoff) {
				delete(recentCodes, existingCode)
			}
		}
	}

	return true
}

func claimWithMainToken(code NitroCode) {
	// Add random delay to make claiming appear more human-like, unless in speed mode
	if !config.SpeedMode {
		delay := time.Duration(rand.Intn(int(config.MaxClaimDelay-config.MinClaimDelay))) + config.MinClaimDelay
		time.Sleep(delay)
	}
	
	startTime := time.Now()

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.Header.Set("Authorization", mainToken)
	// Add random user-agent to mimic different clients
	req.Header.Set("User-Agent", getRandomUserAgent())
	if debugMode {
		log.Printf("[DEBUG] Attempting to claim code %s with main token", code.Code)
	}
	req.SetBodyString(`{"channel_id":null,"payment_source_id":null}`)
	req.SetRequestURI("https://discord.com/api/v9/entitlements/gift-codes/" + code.Code + "/redeem")

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	// Use the global optimized HTTP client
	err := globalHTTPClient.Do(req, resp)
	atomic.AddInt64(&totalClaimAttempts, 1)
	endToEnd := time.Since(code.Timestamp)
	if err != nil {
		mainPing5 := getSessionPing(mainSession)
		log.Printf("[%s] Request error for code %s: %v detect_lag_ms=%d redeem_rtt_ms=%d end_to_end_ms=%d main_ping=%s",
			code.Source, code.Code, err, code.DetectLag.Milliseconds(), time.Since(startTime).Milliseconds(), endToEnd.Milliseconds(), mainPing5)

		mainPing1 := getSessionPing(mainSession)
		// Send webhook notification for request errors
		go sendWebhook(fmt.Sprintf("[%s] REQUEST ERROR for Nitro code: `%s`\nError: %v\nDetect lag: %d ms\nRedeem RTT: %d ms\nEnd-to-end: %d ms\nMain Token Ping: %s\nSource: %s\nSender: %s (%s)\nChannel: %s\nGuild: %s",
			code.Source, code.Code, err, code.DetectLag.Milliseconds(), time.Since(startTime).Milliseconds(), endToEnd.Milliseconds(), mainPing1, code.Source, code.AuthorUsername, code.AuthorID, code.ChannelID, code.GuildID))
		return
	}

	responseTime := time.Since(startTime)
	body := resp.Body()
	statusCode := resp.StatusCode()
	apiCode, apiMessage := parseDiscordAPIError(body)

	// Log response
	if statusCode == 200 {
		mainPing := getSessionPing(mainSession)
		log.Printf("[%s] SUCCESS claimed code %s status=%d detect_lag_ms=%d redeem_rtt_ms=%d end_to_end_ms=%d main_ping=%s",
			code.Source, code.Code, statusCode, code.DetectLag.Milliseconds(), responseTime.Milliseconds(), endToEnd.Milliseconds(), mainPing)

		// Update stats for successful claims
		atomic.AddInt64(&totalClaims, 1)

		mainPing2 := getSessionPing(mainSession)
		// Send success webhook
		go sendWebhook(fmt.Sprintf("[%s] SUCCESSFULLY claimed Nitro code: `%s`\nDetect lag: %d ms\nRedeem RTT: %d ms\nEnd-to-end: %d ms\nMain Token Ping: %s\nStatus Code: %d\nSource: %s\nSender: %s (%s)\nChannel: %s\nGuild: %s",
			code.Source, code.Code, code.DetectLag.Milliseconds(), responseTime.Milliseconds(), endToEnd.Milliseconds(), mainPing2, statusCode, code.Source, code.AuthorUsername, code.AuthorID, code.ChannelID, code.GuildID))
	} else {
		if isNonActionableClaimFailure(statusCode, apiCode) {
			if apiCode == 10038 {
				atomic.AddInt64(&totalUnknownCodes, 1)
				mainPing3 := getSessionPing(mainSession)
				log.Printf("[%s] MISS unknown code %s api_code=%d detect_lag_ms=%d redeem_rtt_ms=%d end_to_end_ms=%d main_ping=%s",
					code.Source, code.Code, apiCode, code.DetectLag.Milliseconds(), responseTime.Milliseconds(), endToEnd.Milliseconds(), mainPing3)
			} else {
				atomic.AddInt64(&totalAlreadyClaimed, 1)
				mainPing4 := getSessionPing(mainSession)
				log.Printf("[%s] MISS already claimed %s api_code=%d detect_lag_ms=%d redeem_rtt_ms=%d end_to_end_ms=%d main_ping=%s",
					code.Source, code.Code, apiCode, code.DetectLag.Milliseconds(), responseTime.Milliseconds(), endToEnd.Milliseconds(), mainPing4)
			}
			return
		}

		mainPing6 := getSessionPing(mainSession)
		log.Printf("[%s] FAILED to claim code %s status=%d api_code=%d api_message=%q detect_lag_ms=%d redeem_rtt_ms=%d end_to_end_ms=%d main_ping=%s body=%s",
			code.Source, code.Code, statusCode, apiCode, apiMessage, code.DetectLag.Milliseconds(), responseTime.Milliseconds(), endToEnd.Milliseconds(), mainPing6, truncateForLog(string(body), 300))

		// Send webhook only for actionable failures.
		go sendWebhook(fmt.Sprintf("[%s] FAILED to claim Nitro code: `%s`\nDetect lag: %d ms\nRedeem RTT: %d ms\nEnd-to-end: %d ms\nStatus Code: %d\nAPI Code: %d\nAPI Message: %s\nResponse: %s\nSource: %s\nSender: %s (%s)\nChannel: %s\nGuild: %s",
			code.Source, code.Code, code.DetectLag.Milliseconds(), responseTime.Milliseconds(), endToEnd.Milliseconds(), statusCode, apiCode, apiMessage, truncateForLog(string(body), 500), code.Source, code.AuthorUsername, code.AuthorID, code.ChannelID, code.GuildID))
	}
}

func parseDiscordAPIError(body []byte) (int, string) {
	var payload struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, ""
	}
	return payload.Code, payload.Message
}

func getRandomUserAgent() string {
	userAgents := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:121.0) Gecko/20100101 Firefox/121.0",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Safari/605.1.15",
	}
	return userAgents[rand.Intn(len(userAgents))]
}

func isNonActionableClaimFailure(statusCode, apiCode int) bool {
	return (statusCode == 404 && apiCode == 10038) || (statusCode == 400 && apiCode == 50050)
}

func truncateForLog(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func sendWebhook(message string) {
	if webhookURL == "" {
		log.Println("Webhook URL not set, skipping notification")
		return
	}

	// Parse message to extract type and content for embed
	var title, description, color string
	lowerMessage := strings.ToLower(message)

	if strings.Contains(lowerMessage, "success") || strings.Contains(lowerMessage, "claimed") {
		title = " Nitro Claim Success"
		description = message
		color = "3066993" // Green
	} else if strings.Contains(lowerMessage, "failed") || strings.Contains(lowerMessage, "request error") || strings.Contains(lowerMessage, "unknown gift code") {
		title = "Nitro Claim Failed"
		description = message
		color = "15158332" // Red
	} else if strings.Contains(lowerMessage, "found") || strings.Contains(lowerMessage, "spotted") {
		title = "Nitro Code Found"
		description = message
		color = "16750848" // Yellow
	} else if strings.Contains(lowerMessage, "connected") {
		title = "Connection Status"
		description = message
		color = "8454143" // Blue
	} else if strings.Contains(lowerMessage, "error") {
		title = "Error Occurred"
		description = message
		color = "15158332" // Red
	} else {
		title = "King Sniper Notification"
		description = message
		color = "10181046" // Purple
	}

	// Extract ping information if present in the message
	pingMatch := ""
	if strings.Contains(description, "Main Token Ping: ") {
		parts := strings.Split(description, "\n")
		for _, part := range parts {
			if strings.Contains(part, "Main Token Ping: ") {
				// Extract ping value
				pingPart := strings.Replace(part, "Main Token Ping: ", "", 1)
				pingMatch = fmt.Sprintf("\n\n **Ping**: %s", pingPart)
				break
			}
		}
	}
	
	// Extract sender and source information if present in the message
	senderMatch := ""
	if strings.Contains(description, "\nSender: ") {
		parts := strings.Split(description, "\n")
		var senderInfo, channelInfo, guildInfo string
		for _, part := range parts {
			if strings.HasPrefix(part, "Sender: ") {
				senderInfo = strings.Replace(part, "Sender: ", "", 1)
			} else if strings.HasPrefix(part, "Channel: ") {
				channelInfo = strings.Replace(part, "Channel: ", "", 1)
			} else if strings.HasPrefix(part, "Guild: ") {
				guildInfo = strings.Replace(part, "Guild: ", "", 1)
			}
		}
		
		if senderInfo != "" {
			senderMatch = fmt.Sprintf("\n\n:user: **Sender**: %s", senderInfo)
		}
		if channelInfo != "" {
			senderMatch += fmt.Sprintf("\n:satellite: **Channel**: %s", channelInfo)
		}
		if guildInfo != "" {
			senderMatch += fmt.Sprintf("\n:globe_with_meridians: **Guild**: %s", guildInfo)
		}
	}
	
	// Append info to description if found
	fullDescription := description
	if pingMatch != "" {
		fullDescription = strings.Replace(fullDescription, pingMatch, "", -1) // Remove duplicate if exists
		fullDescription += pingMatch
	}
	if senderMatch != "" {
		// Remove original sender info lines
		lines := strings.Split(fullDescription, "\n")
		var filteredLines []string
		for _, line := range lines {
			if !strings.HasPrefix(line, "Sender: ") && !strings.HasPrefix(line, "Channel: ") && !strings.HasPrefix(line, "Guild: ") {
				filteredLines = append(filteredLines, line)
			}
		}
		fullDescription = strings.Join(filteredLines, "\n")
		// Add formatted sender info
		fullDescription += senderMatch
	}
	
	// Create embed JSON structure
	embedData := fmt.Sprintf(`{
		"username": "King Sniper",
		"avatar_url": "https://i.pinimg.com/1200x/ac/b9/33/acb933175f444100365ea061814840f8.jpg",
		"embeds": [{
			"title": %s,
			"description": %s,
			"color": %s,
			"timestamp": "%s"
		}]
	}`, string(escapeJSON(title)), string(escapeJSON(fullDescription)), color, time.Now().Format(time.RFC3339))

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	req.SetBodyString(embedData)
	req.SetRequestURI(webhookURL)

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	err := webhookClient.Do(req, resp) // Check for errors
	if err != nil {
		log.Printf("Webhook send error: %v", err)
	} else {
		statusCode := resp.StatusCode()
		if statusCode >= 400 {
			log.Printf("Webhook HTTP error: %d - %s", statusCode, string(resp.Body()))
		} else {
			log.Printf("Webhook sent successfully with status: %d", statusCode)
		}
	}
}

// Helper function to properly escape JSON strings
func escapeJSON(s string) []byte {
	b, _ := json.Marshal(s)
	return b
}

func reportStats() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		codes := atomic.LoadInt64(&totalCodes)
		claims := atomic.LoadInt64(&totalClaims)
		attempts := atomic.LoadInt64(&totalClaimAttempts)
		unknown := atomic.LoadInt64(&totalUnknownCodes)
		already := atomic.LoadInt64(&totalAlreadyClaimed)
		log.Printf("Stats - Codes found: %d, Claims succeeded: %d, Total attempts: %d, Unknown: %d, Already claimed: %d", codes, claims, attempts, unknown, already)
	}
}

// Check if the session has permissions to read messages in a specific channel
func hasReadPermissions(s *discordgo.Session, channelID string) bool {
	// For user accounts (selfbots), permissions work differently than for bot accounts.
	// We'll try to get channel info to check if we have access.
	_, err := s.Channel(channelID)
	return err == nil
}

// Subscribe to all channels to ensure the bot receives message events from all channels
func subscribeToAllChannels(session *discordgo.Session) {
	time.Sleep(5 * time.Second) // Wait a bit for initial connection to settle
	
	log.Println("Attempting to access all channels to ensure message events...")
	
	// Get all guilds the bot is part of - correct function signature
	guilds, err := session.UserGuilds(100, "", "", false)
	if err != nil {
		log.Printf("Error getting guilds: %v", err)
		return
	}
	
	for _, guild := range guilds {
		// Get all channels in the guild
		channels, err := session.GuildChannels(guild.ID)
		if err != nil {
			log.Printf("Error getting channels for guild %s: %v", guild.Name, err)
			continue
		}
		
		for _, channel := range channels {
			// For selfbot, we need to specifically request to join the voice channel or trigger activity
			// Accessing the channel should be enough to receive messages in it
			chanInfo, err := session.Channel(channel.ID)
			if err != nil {
				if channelScanningLogging {
					log.Printf("Could not access channel %s in guild %s: %v", channel.Name, guild.Name, err)
				}
			} else {
				// Check if it's a text channel
				if chanInfo.Type == discordgo.ChannelTypeGuildText {
					if channelScanningLogging {
							log.Printf("Successfully registered interest in channel: %s/%s", guild.Name, channel.Name)
						}
					// Try to set our viewing status in the channel to ensure we receive updates
					trySetViewingStatus(session, chanInfo.ID)
				}
			}
		}
	}
	
	log.Println("Completed channel subscription process")
}

// Helper function to try to set viewing status in a channel
func trySetViewingStatus(session *discordgo.Session, channelID string) {
	// For selfbots, we may need to simulate user activity to ensure we receive messages
	// This makes the client appear active in the channel
	err := session.UpdateStatusComplex(discordgo.UpdateStatusData{
		Status: "online",
		Activities: []*discordgo.Activity{
			{
				Type: 0, // Using 0 as default activity type (playing game)
				Name: "on Discord",
			},
		},
		AFK: false,
	})
	if err != nil {
		log.Printf("Could not update status: %v", err)
	}
}
