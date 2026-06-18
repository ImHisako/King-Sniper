# King Sniper - Safe Nitro Sniping Guide

## Anti-Detection Improvements

This sniper has been enhanced with several anti-detection mechanisms to avoid Discord's "Platform manipulation" flagging:

### Key Improvements:
1. **Random Delays**: Added random delays between operations to appear more human-like
2. **Variable User-Agents**: Uses different browser/OS combinations randomly
3. **Reduced Connection Rates**: Lowered concurrent connections and spaced out operations
4. **Rate Limiting**: Implemented proper delays between claims and notifications
5. **Less Aggressive Scanning**: Extended refresh intervals and added delays

## Usage Modes

This sniper offers two operational modes:

### Safe Mode (Default)
- Includes delays and anti-detection measures
- Lower chance of account flagging
- Slightly slower claim times

### Speed Mode
- Removes most delays for maximum claiming speed
- Higher risk of account flagging
- Enabled by setting SPEED_MODE=true

## Safe Usage Guidelines

To minimize account flagging risk in Safe Mode:

### 1. Limit Active Accounts
- Use no more than 2-3 alt accounts simultaneously
- Consider using accounts with verified phone numbers
- Use accounts that have been active for at least 30 days

### 2. Geographic Considerations
- Use residential proxies instead of datacenter IPs when possible
- Ensure accounts are spread across different geographic locations
- Match IP geolocation with account registration region

### 3. Activity Patterns
- Vary usage times (don't run 24/7)
- Occasionally manually interact with accounts
- Don't immediately claim every code found

### 4. Hardware/Software Fingerprinting
- Use different browsers, OS versions, and hardware specs for accounts
- Install different browser extensions on each account
- Vary screen resolutions and time zones

## Installation

1. Make sure you have Go installed (version 1.18 or higher)
2. Clone this repository
3. Run `go mod tidy` to install dependencies
4. Set up your tokens in `.env` or `tokens.txt`

## Environment Setup

Create a `.env` file with your tokens:
```
MAIN_TOKEN=your_main_account_token
ALT_TOKEN_1=first_alt_account_token
ALT_TOKEN_2=second_alt_account_token
WEBHOOK_URL=your_discord_webhook_url
```

Or create a `tokens.txt` file:
```
MAIN_TOKEN=your_main_account_token
ALT_TOKEN_1=first_alt_account_token
ALT_TOKEN_2=second_alt_account_token
WEBHOOK_URL=your_discord_webhook_url
```

## Running the Sniper

Compile and run:
```bash
go build -o sniper .
./sniper
```

Or run directly:
```bash
go run main.go
```

## Important Warnings

1. **Account Safety**: Using multiple accounts violates Discord's Terms of Service and can result in account termination
2. **Legal Compliance**: Only use this software in accordance with applicable laws
3. **Risk Acknowledgment**: You accept all risks associated with using this software
4. **Rate Limiting**: Even with improvements, detection is still possible with aggressive usage

## Troubleshooting

If you encounter issues:
- Check that all tokens are valid and properly formatted
- Verify your webhook URL is correct
- Ensure your firewall allows the application to make outbound connections
- Monitor logs for specific error messages

## Contributing

This project is for educational purposes only. Contributions should focus on improving safety and reliability while respecting Discord's platform policies.