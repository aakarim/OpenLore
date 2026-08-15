package cmds

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

func CmdDate(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	useUTC := false
	var format string

	for _, a := range args {
		if a == "-u" {
			useUTC = true
		} else if strings.HasPrefix(a, "+") {
			format = a[1:]
		} else if len(a) > 1 && a[0] == '-' {
			ReportUnsupportedFlag(ctx, "date", a)
		}
	}

	t := time.Now()
	if useUTC {
		t = t.UTC()
	}

	if format == "" {
		fmt.Fprintln(w, t.Format("Mon Jan  2 15:04:05 MST 2006"))
		return 0
	}

	fmt.Fprintln(w, dateFormat(t, format))
	return 0
}

func dateFormat(t time.Time, format string) string {
	var sb strings.Builder
	i := 0
	for i < len(format) {
		if format[i] == '%' && i+1 < len(format) {
			i++
			switch format[i] {
			case 'Y':
				sb.WriteString(fmt.Sprintf("%04d", t.Year()))
			case 'm':
				sb.WriteString(fmt.Sprintf("%02d", t.Month()))
			case 'd':
				sb.WriteString(fmt.Sprintf("%02d", t.Day()))
			case 'H':
				sb.WriteString(fmt.Sprintf("%02d", t.Hour()))
			case 'M':
				sb.WriteString(fmt.Sprintf("%02d", t.Minute()))
			case 'S':
				sb.WriteString(fmt.Sprintf("%02d", t.Second()))
			case 'A':
				sb.WriteString(t.Weekday().String())
			case 'a':
				sb.WriteString(t.Weekday().String()[:3])
			case 'B':
				sb.WriteString(t.Month().String())
			case 'b':
				sb.WriteString(t.Month().String()[:3])
			case 'Z':
				zone, _ := t.Zone()
				sb.WriteString(zone)
			case 's':
				sb.WriteString(strconv.FormatInt(t.Unix(), 10))
			case 'N':
				sb.WriteString(fmt.Sprintf("%09d", t.Nanosecond()))
			case 'j':
				sb.WriteString(fmt.Sprintf("%03d", t.YearDay()))
			case 'u':
				day := int(t.Weekday())
				if day == 0 {
					day = 7
				}
				sb.WriteString(strconv.Itoa(day))
			case 'p':
				if t.Hour() < 12 {
					sb.WriteString("AM")
				} else {
					sb.WriteString("PM")
				}
			case '%':
				sb.WriteByte('%')
			default:
				sb.WriteByte('%')
				sb.WriteByte(format[i])
			}
		} else {
			sb.WriteByte(format[i])
		}
		i++
	}
	return sb.String()
}
