# Example Terraform configuration for Magic Mirror
# This demonstrates how to use the Magic Mirror Terraform provider

terraform {
  required_providers {
    magicmirror = {
      source  = "skyler/magicmirror"
      version = "~> 0.1"
    }
  }
}

# Configure the provider to connect to the Magic Mirror Agent
provider "magicmirror" {
  host    = var.magicmirror_host
  port    = var.magicmirror_port
  api_key = var.magicmirror_api_key
}

# Global Magic Mirror configuration
resource "magicmirror_config" "main" {
  address     = "0.0.0.0"
  port        = 8080
  language    = "en"
  time_format = 12
  units       = "imperial"

  ip_whitelist = [
    "127.0.0.1",
    "::ffff:127.0.0.1",
    "::1",
    "192.168.1.0/24"
  ]
}

# Clock module - displays time in the top left
resource "magicmirror_module" "clock" {
  module   = "clock"
  position = "top_left"

  config = jsonencode({
    displaySeconds = true
    showPeriod     = true
    showDate       = true
    dateFormat     = "dddd, MMMM D"
  })
}

# Weather module - current weather conditions
resource "magicmirror_module" "weather_current" {
  module   = "weather"
  position = "top_right"
  header   = "Current Weather"

  config = jsonencode({
    weatherProvider = "openweathermap"
    type            = "current"
    location        = var.weather_location
    apiKey          = var.openweathermap_api_key
    units           = "imperial"
    showHumidity    = true
    showWindSpeed   = true
  })
}

# Weather forecast module
resource "magicmirror_module" "weather_forecast" {
  module   = "weather"
  position = "top_right"
  header   = "Forecast"

  config = jsonencode({
    weatherProvider = "openweathermap"
    type            = "forecast"
    location        = var.weather_location
    apiKey          = var.openweathermap_api_key
    units           = "imperial"
    maxNumberOfDays = 5
  })
}

# Calendar module - shows upcoming events
resource "magicmirror_module" "calendar" {
  module   = "calendar"
  position = "top_left"
  header   = "Upcoming Events"

  config = jsonencode({
    calendars = [
      {
        symbol = "calendar"
        url    = var.calendar_url
      }
    ]
    maximumEntries         = 10
    maximumNumberOfDays    = 14
    showLocation           = true
    wrapEvents             = true
    displaySymbol          = true
  })
}

# Compliments module - displays random compliments
resource "magicmirror_module" "compliments" {
  module   = "compliments"
  position = "lower_third"

  config = jsonencode({
    compliments = {
      anytime  = ["Hey there handsome!"]
      morning  = ["Good morning!", "Rise and shine!"]
      afternoon = ["Looking good!", "Keep up the great work!"]
      evening  = ["Relax and unwind", "You made it through another day!"]
    }
  })
}

# News feed module
resource "magicmirror_module" "newsfeed" {
  module   = "newsfeed"
  position = "bottom_bar"

  config = jsonencode({
    feeds = [
      {
        title = "BBC News"
        url   = "https://feeds.bbci.co.uk/news/rss.xml"
      },
      {
        title = "Hacker News"
        url   = "https://hnrss.org/frontpage"
      }
    ]
    showSourceTitle  = true
    showPublishDate  = true
    broadcastNewsFeeds = true
    broadcastNewsUpdates = true
    showDescription  = true
    maxNewsItems     = 5
  })
}
