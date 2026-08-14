import 'package:intl/date_symbol_data_local.dart';
import 'package:intl/intl.dart';

/// Two independent traps, and the first one is easy to misdiagnose because
/// the library is inconsistent about it on purpose.
///
/// 1. DateFormat for any locale other than the default throws
///    LocaleDataException until initializeDateFormatting() has run. But
///    NumberFormat for the same locale works immediately, because number
///    symbols ship with the package and date symbols are loaded on demand.
///    So "intl works for de but not for de" is the expected behaviour.
///
/// 2. DateFormat.parse accepts an impossible date and rolls it over
///    silently: 2026-02-30 becomes 2026-03-02. parseStrict is the one that
///    reports it.
Future<void> loadLocales() => initializeDateFormatting();

String formatIn(String locale, DateTime when) =>
    DateFormat.yMMMMd(locale).format(when);

/// Number formatting needs no initialization at all.
String numberIn(String locale, num value) =>
    NumberFormat.decimalPattern(locale).format(value);

DateTime parseLoose(String text) => DateFormat('yyyy-MM-dd').parse(text);

DateTime parseStrict(String text) => DateFormat('yyyy-MM-dd').parseStrict(text);
