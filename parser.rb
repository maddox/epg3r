require 'date'
require 'digest'
require 'open-uri'
require 'rexml/document'
require 'fileutils'

# Game times in the playlist are always Eastern Time. Compute the ET offset for a
# given wall-clock time without depending on the container's TZ or tzdata.
# US DST runs from 2:00 AM on the second Sunday in March to 2:00 AM on the first
# Sunday in November.
def eastern_offset(year, month, day, hour)
  march = Date.new(year, 3, 1)
  dst_start = march + ((7 - march.wday) % 7) + 7
  november = Date.new(year, 11, 1)
  dst_end = november + ((7 - november.wday) % 7)

  date = Date.new(year, month, day)
  in_dst = (date > dst_start || (date == dst_start && hour >= 2)) &&
           (date < dst_end || (date == dst_end && hour < 2))
  in_dst ? "-04:00" : "-05:00"
end

# Month arrives as "09" from the first title format or "Oct" from the second.
def parse_month(text)
  return text.to_i if text =~ /\A\d+\z/
  Date::ABBR_MONTHNAMES.index { |name| name && name.casecmp?(text[0, 3]) }
end

# The playlist only lists month and day. Pick the year that puts the game closest
# to today so December runs handle January games and vice versa.
def infer_year(month, day)
  today = Date.today
  candidates = [today.year - 1, today.year, today.year + 1].map do |year|
    Date.valid_date?(year, month, day) ? Date.new(year, month, day) : nil
  end.compact
  candidates.min_by { |date| (date - today).abs }.year
end

input_m3u_path = "iptv.m3u"
output_m3u_path = "iptv-sports.m3u"
output_epg_path = "iptv-sports.xml"
iptv_m3u_url = ENV['M3U_URL']

# clean up old files
FileUtils.rm(input_m3u_path) if File.exist?(input_m3u_path)
FileUtils.rm(output_m3u_path) if File.exist?(output_m3u_path)
FileUtils.rm(output_epg_path) if File.exist?(output_epg_path)

iptv_m3u_data = URI.open(iptv_m3u_url).read
File.write(input_m3u_path, iptv_m3u_data)

epg_matcher = Regexp.new(/^(#\S+(?:\s+[^\s="]+=".*")+),(.*)\s*(.*)\s*(http.*)/)

leagues = {
            "NFL": {
              prefix: "NFL",
              starting_channel_number: 8500,
              series_id: "191277",
              channel_logo_url: "http://static.maddox.casa/channels/clearart/sunday-ticket-clearart.png",
              airing_placard_url: "http://static.maddox.casa/channels/placard/nfl-football-sunday-ticket.jpg",
              airing_title: "NFL Football",
              duration: (60*60*3.5),
              genres: ["Football"]
            },
            "MLB Baseball league":  {
              prefix: "MLB",
              starting_channel_number: 8600,
              series_id: "191273",
              channel_logo_url: "http://static.maddox.casa/channels/clearart/mlb-clearart.png",
              airing_placard_url: "http://static.maddox.casa/channels/placard/mlb-baseball.jpg",
              airing_title: "MLB Baseball",
              duration: (60*60*3.5),
              genres: ["Baseball"]
            },
            "MLS":  {
              prefix: "MLS",
              starting_channel_number: 8700,
              series_id: "191274",
              channel_logo_url: "http://static.maddox.casa/channels/clearart/mls-clearart.png",
              airing_placard_url: "http://static.maddox.casa/channels/placard/mls-soccer.jpg",
              airing_title: "MLS Soccer",
              duration: (60*60*2),
              genres: ["Soccer"]
            },
            "NBA":  {
              prefix: "NBA",
              starting_channel_number: 8800,
              series_id: "191276",
              channel_logo_url: "http://static.maddox.casa/channels/clearart/nba-clearart.png",
              airing_placard_url: "http://static.maddox.casa/channels/placard/nba-basketball.jpg",
              airing_title: "NBA Basketball",
              duration: (60*60*3.5),
              genres: ["Basketball"]
            }
          }

m3u = open(input_m3u_path).read
matches = m3u.scan(epg_matcher)
channels = []

matches.sort_by! { |match| match[1] } # Sort by channel id

matches.each do |match|
  title = match[1]
  stream_url = match[3]
  group_matches = match[0].match(/group-title="([^"]+)/)

  
  group = group_matches[1]

  league = leagues[group.to_sym]
  next unless league

  # NFL 03: Pittsburgh Steelers vs Atlanta Falcons (09.08 1:00PM ET) (FOX)
  regex1 = Regexp.new(/(\w+ \d+):? ?\|? (.*) \((\d\d).(\d\d) (.*):(.*)(AM|PM) ET\)/)
  # NFL 15: Los Angeles Chargers vs Chicago Bears Oct 29 08:20 PM
  regex2 = Regexp.new(/(\w+ \d+): (.*) (\w+) (\d+) (.*):(.*) (AM|PM)/)

  title_match = title.match(regex1)
  if !title_match
    title_match = title.match(regex2)
  end

  puts title
  puts title_match.inspect
  puts
  next unless title_match

  channel_id = title_match[1]
  event_title = title_match[2]
  month = parse_month(title_match[3])
  day = title_match[4].to_i
  hour = title_match[5].to_i
  minute = title_match[6].to_i
  ampm = title_match[7]

  hour = hour + 12 if ampm == "PM" && hour != 12
  hour = 0 if ampm == "AM" && hour == 12
  year = infer_year(month, day)
  time = Time.new(year, month, day, hour, minute, 0, eastern_offset(year, month, day, hour))

  start_time = (time - 15*60).utc
  end_time = Time.at((time.to_i + league[:duration])).utc

  channel_number = league[:starting_channel_number] + channel_id.gsub(league[:prefix], '').to_i
  channels << { channel_id: channel_id, 
                channel_number: channel_number,
                channel_logo_url: league[:channel_logo_url],
                stream_url: stream_url,
                program: { 
                          airing_placard_url: league[:airing_placard_url], 
                          airing_title: league[:airing_title],
                          series_id: league[:series_id], 
                          episode_id: "#{league[:series_id]}-#{Digest::MD5.hexdigest(event_title)[0, 12]}", 
                          genres: league[:genres],
                          event_title: event_title, 
                          start_time: start_time,
                          end_time: end_time 
                        }
              }

end


m3u_output = "#EXTM3U\n\n"
epg_doc = REXML::Document.new
epg_doc << REXML::XMLDecl.new('1.0', 'utf-8')
epg_doc << REXML::DocType.new('tv SYSTEM "xmltv.dtd"')
tv = epg_doc.add_element 'tv'

channels.each do |channel|
  channel_id = channel[:channel_id]
  program = channel[:program]

  # write to m3u
  m3u_output += "#EXTINF:-1 channel-id=\"#{channel_id}\" tvg-id=\"#{channel_id}\" channel-number=\"#{channel[:channel_number]}\" tvg-name=\"#{channel_id}\" tvg-logo=\"#{channel[:channel_logo_url]}\",#{channel_id}\n"
  m3u_output += "#{channel[:stream_url]}\n\n"

  # write to xmltv EPG
  channel_el = tv.add_element 'channel'
  channel_el.add_attribute 'id', channel_id
  display_name = channel_el.add_element 'display-name'
  display_name.add_text(channel_id)
  icon = channel_el.add_element 'icon'
  icon.attributes['src'] = channel[:channel_logo_url]

  start_stamp = program[:start_time].strftime("%Y%m%d%H%M%S %z")
  stop_stamp = program[:end_time].strftime("%Y%m%d%H%M%S %z")

  programme = tv.add_element 'programme'
  programme.add_attribute 'start', start_stamp
  programme.add_attribute 'stop', stop_stamp
  programme.add_attribute 'channel', channel_id
  title = programme.add_element 'title'
  title.add_text(program[:airing_title])

  subtitle = programme.add_element 'sub-title'
  subtitle.add_text(program[:event_title])

  desc = programme.add_element 'desc'
  desc.add_text("#{program[:airing_title]} presents #{program[:event_title]}")

  series_id = programme.add_element 'series-id'
  series_id.add_text(program[:series_id])

  # episode_num = programme.add_element 'episode-num'
  # episode_num.add_attribute 'system', 'original-air-date'
  # episode_num.add_text(program[:start_time].strftime("%Y-%m-%d"))

  episode_num = programme.add_element 'episode-num'
  episode_num.add_attribute 'system', ''
  episode_num.add_text(program[:episode_id])

  date = programme.add_element 'date'
  date.add_text(program[:start_time].strftime("%Y-%m-%d"))

  icon = programme.add_element 'icon'
  icon.add_attribute 'src', program[:airing_placard_url]

  video = programme.add_element 'video'
  quality = video.add_element 'quality'
  quality.add_text('HDTV')

  icon = programme.add_element 'new'
  icon = programme.add_element 'live'

  genres = program[:genres]
  categories = ["Sports event", "Sports"]

  (genres+categories).each do |category|
    tag = programme.add_element 'category'
    tag.add_attribute 'lang', 'en'
    tag.add_text(category)
  end

end


  File.write(output_m3u_path, m3u_output)
  File.write(output_epg_path, epg_doc.to_s)
