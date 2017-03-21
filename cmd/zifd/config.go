/*
 *  Zif
 *  Copyright (C) 2017 Zif LTD
 *
 *  This program is free software: you can redistribute it and/or modify
 *  it under the terms of the GNU Affero General Public License as published
 *  by the Free Software Foundation, either version 3 of the License, or
 *  (at your option) any later version.
 *
 *  This program is distributed in the hope that it will be useful,
 *  but WITHOUT ANY WARRANTY; without even the implied warranty of
 *  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *  GNU Affero General Public License for more details.

 *  You should have received a copy of the GNU Affero General Public License
 *  along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */
package main

import (
	"fmt"

	"github.com/fsnotify/fsnotify"
	log "github.com/sirupsen/logrus"
	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
)

func SetupConfig() {
	// bind the "bind" flags
	flag.String("bind", "0.0.0.0:5050", "The address and port to listen for zif protocol connections")
	flag.String("http", "127.0.0.1:8080", "The address and port to listen on for http commands")
	flag.Parse()

	viper.BindPFlag("bind.zif", flag.Lookup("bind"))
	viper.BindPFlag("bind.http", flag.Lookup("http"))

	viper.SetConfigName("zifd")
	viper.AddConfigPath(".")
	viper.AddConfigPath("$HOME/.zif")
	viper.AddConfigPath("/etc/zif")

	err := viper.ReadInConfig()

	if err != nil {
		panic(fmt.Errorf("Fatal error loading config file: %s \n", err))
	}

	viper.SetDefault("bind", map[string]string{
		"zif":  "0.0.0.0:5050",
		"http": "127.0.0.1:8080",
	})

	// someday support postgresql, etc. Hence the map :)
	viper.SetDefault("database", map[string]string{
		"path": "./data/posts.db",
	})

	viper.SetDefault("tor", map[string]interface{}{
		"enabled":    true,
		"control":    10051,
		"socks":      10050,
		"cookiePath": "./tor/",
	})

	viper.SetDefault("socks", map[string]interface{}{"enabled": true, "port": 10050})

	viper.SetDefault("net", map[string]interface{}{
		"maxPeers": 100,
	})

	viper.WatchConfig()

	viper.OnConfigChange(func(e fsnotify.Event) {
		log.Info("Config file changed, reloading: ", e.Name)
	})
}
